package incident

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed web/*
var webAssets embed.FS

type Server struct {
	tools     *Tools
	commander *Commander
	web       fs.FS
}

func NewServer(tools *Tools, commander *Commander) (*Server, error) {
	web, err := fs.Sub(webAssets, "web")
	if err != nil {
		return nil, err
	}
	return &Server{tools: tools, commander: commander, web: web}, nil
}

func (s *Server) Handler(_ context.Context) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tools/metrics", s.getMetrics)
	mux.HandleFunc("GET /api/tools/logs", s.searchLogs)
	mux.HandleFunc("GET /api/tools/deployments", s.getDeployments)
	mux.HandleFunc("GET /api/tools/code", s.searchCode)
	mux.HandleFunc("GET /api/tools/file", s.readFile)
	mux.HandleFunc("GET /api/tools/diff", s.gitDiff)
	mux.HandleFunc("GET /api/tools/knowledge", s.searchKnowledge)
	mux.HandleFunc("POST /api/investigate", s.investigate)
	mux.HandleFunc("POST /api/rollback/approve", s.approveRollback)
	mux.HandleFunc("POST /api/reset", s.reset)
	mux.Handle("GET /", http.FileServer(http.FS(s.web)))
	return mux
}

func (s *Server) getMetrics(w http.ResponseWriter, _ *http.Request) {
	metrics, err := s.tools.GetMetrics()
	writeResult(w, metrics, err)
}

func (s *Server) searchLogs(w http.ResponseWriter, r *http.Request) {
	logs, err := s.tools.SearchLogs(r.URL.Query().Get("q"))
	writeResult(w, logs, err)
}

func (s *Server) getDeployments(w http.ResponseWriter, _ *http.Request) {
	deployments, err := s.tools.GetDeployments()
	writeResult(w, deployments, err)
}

func (s *Server) searchCode(w http.ResponseWriter, r *http.Request) {
	matches, err := s.tools.SearchCode(r.URL.Query().Get("q"))
	writeResult(w, matches, err)
}

func (s *Server) readFile(w http.ResponseWriter, r *http.Request) {
	contents, err := s.tools.ReadFile(r.URL.Query().Get("path"))
	writeResult(w, contents, err)
}

func (s *Server) gitDiff(w http.ResponseWriter, _ *http.Request) {
	diff, err := s.tools.GitDiff()
	writeResult(w, diff, err)
}

func (s *Server) searchKnowledge(w http.ResponseWriter, r *http.Request) {
	results, err := s.tools.SearchKnowledge(r.URL.Query().Get("q"))
	writeResult(w, results, err)
}

type incidentRequest struct {
	Incident string `json:"incident"`
}

func (s *Server) investigate(w http.ResponseWriter, r *http.Request) {
	request, err := decodeRequest(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	result, err := s.commander.Investigate(r.Context(), request.Incident)
	writeResult(w, result, err)
}

func (s *Server) approveRollback(w http.ResponseWriter, r *http.Request) {
	request, err := decodeRequest(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	investigation, postmortem, err := s.commander.ApproveRollback(r.Context(), request.Incident)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Investigation Investigation `json:"investigation"`
		Postmortem    Postmortem    `json:"postmortem"`
	}{investigation, postmortem})
}

func (s *Server) reset(w http.ResponseWriter, _ *http.Request) {
	if err := s.tools.ResetSimulation(); err != nil {
		writeResult(w, nil, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "simulation_reset"})
}

func decodeRequest(r *http.Request) (incidentRequest, error) {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	var request incidentRequest
	if err := decoder.Decode(&request); err != nil {
		return incidentRequest{}, err
	}
	return request, nil
}

func writeResult(w http.ResponseWriter, value any, err error) {
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "must not be empty") || strings.Contains(err.Error(), "must include") || strings.Contains(err.Error(), "relative") || strings.Contains(err.Error(), "escapes") {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
