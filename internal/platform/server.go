package platform

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var assets embed.FS

type Server struct {
	Engine          *Engine
	evalMu          sync.Mutex
	report          any
	projectMu       sync.Mutex
	projectPreviews map[string]projectModelPreview
}

func NewServer(e *Engine) *Server {
	return &Server{Engine: e, projectPreviews: map[string]projectModelPreview{}}
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerLocalProjects(mux)
	mux.HandleFunc("GET /api/v2/status", s.status)
	mux.HandleFunc("GET /api/v2/incidents", func(w http.ResponseWriter, r *http.Request) { v, err := s.Engine.List(); respond(w, v, err) })
	mux.HandleFunc("POST /api/v2/incidents", s.create)
	mux.HandleFunc("GET /api/v2/incidents/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Engine.Get(r.PathValue("id"))
		respond(w, v, err)
	})
	mux.HandleFunc("POST /api/v2/incidents/{id}/investigate", func(w http.ResponseWriter, r *http.Request) {
		var body struct{}
		if !decode(w, r, &body) {
			return
		}
		v, err := s.Engine.Investigate(r.Context(), r.PathValue("id"))
		respond(w, v, err)
	})
	mux.HandleFunc("POST /api/v2/incidents/{id}/actions/{action}/approve", s.approve)
	mux.HandleFunc("POST /api/v2/incidents/{id}/memory/review", s.review)
	mux.HandleFunc("GET /api/v2/memory", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Engine.Store.ListMemories()
		respond(w, v, err)
	})
	mux.HandleFunc("POST /api/v2/tools/{name}", s.tool)
	mux.HandleFunc("GET /api/v2/evaluation", func(w http.ResponseWriter, r *http.Request) {
		s.evalMu.Lock()
		defer s.evalMu.Unlock()
		if s.report == nil {
			respond(w, map[string]string{"status": "not_run"}, nil)
		} else {
			respond(w, s.report, nil)
		}
	})
	mux.HandleFunc("POST /api/v2/evaluation", s.evaluate)
	web, _ := fs.Sub(assets, "web")
	mux.Handle("GET /", http.FileServer(http.FS(web)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !localRequestHost(r) {
			http.Error(w, "only local loopback hosts are permitted", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			if err != nil || u.Host != r.Host || u.Scheme != scheme || u.User != nil {
				http.Error(w, "cross-origin requests are forbidden", http.StatusForbidden)
				return
			}
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "none" && site != "same-origin" {
			http.Error(w, "cross-site requests are forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		if r.Method != "GET" && r.Method != "HEAD" {
			origin := r.Header.Get("Origin")
			if origin != "" {
				u, err := url.Parse(origin)
				scheme := "http"
				if r.TLS != nil {
					scheme = "https"
				}
				if err != nil || u.Host != r.Host || u.Scheme != scheme {
					http.Error(w, "cross-origin writes are forbidden", http.StatusForbidden)
					return
				}
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				http.Error(w, "cross-site writes are forbidden", http.StatusForbidden)
				return
			}
			mediaType, _, parseErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if parseErr != nil || !strings.EqualFold(mediaType, "application/json") {
				http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

// Restrict Host even on GET: an Origin check alone cannot stop DNS rebinding.
func localRequestHost(r *http.Request) bool {
	if strings.ContainsAny(r.Host, "@/\\?#") {
		return false
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
		port = ""
	}
	if host != "localhost" && host != "127.0.0.1" && host != "::1" && host != "[::1]" {
		return false
	}
	if address, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		peer, _, peerErr := net.SplitHostPort(r.RemoteAddr)
		if peerErr != nil || net.ParseIP(peer) == nil || !net.ParseIP(peer).IsLoopback() {
			return false
		}
		_, localPort, err := net.SplitHostPort(address.String())
		if err == nil {
			if port == "" {
				port = "80"
				if r.TLS != nil {
					port = "443"
				}
			}
			if port != localPort {
				return false
			}
		}
	}
	return true
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	defs, err := s.Engine.Backend.List(r.Context())
	if err != nil {
		respond(w, nil, err)
		return
	}
	mode, model := "offline", "未配置模型；离线规则基线"
	if s.Engine.Model != nil {
		mode = "llm"
		model = s.Engine.Model.Provider + " / " + s.Engine.Model.Name
	}
	policy := []PolicyRule{}
	for _, a := range []string{"get_metrics", "search_logs", "search_code", "create_incident_note", "restart_replica", "mitigate_pool", "rollback", "failover_provider", "database_mutation"} {
		policy = append(policy, Policy(a))
	}
	respond(w, map[string]any{"version": "2.0", "mode": mode, "model": model, "tools": defs, "skills": s.Engine.Skills, "policy": policy, "scenarios": s.Engine.Scenarios, "simulation_actions": s.Engine.localRecovery()}, nil)
}
func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ScenarioID string  `json:"scenario_id"`
		Title      string  `json:"title"`
		Mode       string  `json:"mode"`
		Options    Options `json:"options"`
	}
	if !decode(w, r, &request) {
		return
	}
	v, err := s.Engine.Create(request.ScenarioID, request.Title, request.Mode, request.Options)
	respond(w, v, err)
}
func (s *Server) approve(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Approved bool `json:"approved"`
		Revision int  `json:"revision"`
	}
	if !decode(w, r, &request) {
		return
	}
	v, err := s.Engine.Approve(r.Context(), r.PathValue("id"), r.PathValue("action"), request.Approved, request.Revision)
	respond(w, v, err)
}
func (s *Server) review(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Status   string `json:"status"`
		Reviewer string `json:"reviewer"`
	}
	if !decode(w, r, &request) {
		return
	}
	v, err := s.Engine.ReviewMemory(r.PathValue("id"), request.Status, request.Reviewer)
	respond(w, v, err)
}
func (s *Server) tool(w http.ResponseWriter, r *http.Request) {
	var request struct {
		IncidentID string         `json:"incident_id"`
		Arguments  map[string]any `json:"arguments"`
	}
	if !decode(w, r, &request) {
		return
	}
	i, err := s.Engine.Get(request.IncidentID)
	if err != nil {
		respond(w, nil, err)
		return
	}
	raw, err := s.Engine.call(r.Context(), &i, r.PathValue("name"), request.Arguments)
	respond(w, raw, err)
}
func (s *Server) evaluate(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !decode(w, r, &body) {
		return
	}
	if !s.evalMu.TryLock() {
		respond(w, nil, ErrConflict)
		return
	}
	defer s.evalMu.Unlock()
	report, err := RunEvaluation(r.Context(), s.Engine.root)
	if err == nil {
		s.report = report
	}
	respond(w, report, err)
}
func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON request"})
		return false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request must contain exactly one JSON value"})
		return false
	}
	return true
}
func respond(w http.ResponseWriter, value any, err error) {
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrConflict) {
			status = http.StatusConflict
		}
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]any{"error": err.Error(), "incident": value})
		return
	}
	writeJSON(w, http.StatusOK, value)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func HTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
}
