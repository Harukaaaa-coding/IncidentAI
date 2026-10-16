package platform

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Project registration exposes local paths. Origin checks alone do not prevent
// DNS rebinding because a hostile browser origin can become same-origin with a
// listener on loopback. Reject untrusted Host values before routing any API.
func TestLocalProjectHTTPRejectsDNSRebinding(t *testing.T) {
	engine, _, _ := testEngine(t)
	handler := NewServer(engine).Handler()
	for _, target := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v2/status", ""},
		{http.MethodGet, "/api/v2/projects/suggestions", ""},
		{http.MethodPost, "/api/v2/incidents", `{"scenario_id":"deployment-regression"}`},
	} {
		for _, host := range []string{"attacker.example:8099", "localhost.attacker.example:8099", "127.0.0.1.attacker.example:8099", "localhost.:8099", "0.0.0.0:8099", "192.168.1.2:8099"} {
			t.Run(target.method+target.path+"/"+host, func(t *testing.T) {
				req := httptest.NewRequest(target.method, "http://"+host+target.path, strings.NewReader(target.body))
				req.Header.Set("Origin", "http://"+host)
				req.Header.Set("Sec-Fetch-Site", "same-origin")
				req.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, req)
				if response.Code != http.StatusForbidden {
					t.Fatalf("untrusted Host %s received %d: %s", host, response.Code, response.Body.String())
				}
			})
		}
	}
}

func TestLocalProjectHTTPLoopbackOrigins(t *testing.T) {
	engine, _, _ := testEngine(t)
	handler := NewServer(engine).Handler()
	for _, host := range []string{"localhost:8099", "127.0.0.1:8099", "[::1]:8099"} {
		t.Run(host, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+host+"/api/v2/status", nil)
			req.Header.Set("Origin", "http://"+host)
			req.Header.Set("Sec-Fetch-Site", "same-origin")
			req.RemoteAddr = "127.0.0.1:50000"
			req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8099}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != http.StatusOK {
				t.Fatalf("loopback origin rejected: %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestLocalProjectHTTPSensitiveReadsRejectCrossOrigin(t *testing.T) {
	engine, _, _ := testEngine(t)
	handler := NewServer(engine).Handler()
	for _, path := range []string{"/api/v2/status", "/api/v2/projects/suggestions"} {
		for _, tc := range []struct{ name, origin, site string }{
			{"external origin", "http://attacker.example", ""},
			{"other loopback port", "http://localhost:8100", "same-site"},
			{"cross-site no origin", "", "cross-site"},
			{"same-site no origin", "", "same-site"},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, "http://localhost:8099"+path, nil)
				req.Header.Set("Origin", tc.origin)
				req.Header.Set("Sec-Fetch-Site", tc.site)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, req)
				if response.Code != http.StatusForbidden {
					t.Fatalf("cross-origin sensitive read received %d: %s", response.Code, response.Body.String())
				}
			})
		}
	}
}

func TestLocalProjectHTTPRejectsHostPortMismatch(t *testing.T) {
	engine, _, _ := testEngine(t)
	req := httptest.NewRequest(http.MethodGet, "http://localhost:8100/api/v2/status", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8099}))
	response := httptest.NewRecorder()
	NewServer(engine).Handler().ServeHTTP(response, req)
	if response.Code != http.StatusForbidden {
		t.Fatalf("Host port mismatch received %d: %s", response.Code, response.Body.String())
	}
}

func TestLocalProjectHTTPRejectsRemotePeerDespiteLoopbackHost(t *testing.T) {
	engine, _, _ := testEngine(t)
	for _, remote := range []string{"192.0.2.10:40000", "[2001:db8::1]:40000", "malformed"} {
		t.Run(remote, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://localhost:8099/api/v2/status", nil)
			req.RemoteAddr = remote
			req.Header.Set("X-Forwarded-For", "127.0.0.1")
			req.Header.Set("X-Forwarded-Host", "localhost:8099")
			req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("0.0.0.0"), Port: 8099}))
			response := httptest.NewRecorder()
			NewServer(engine).Handler().ServeHTTP(response, req)
			if response.Code != http.StatusForbidden {
				t.Fatalf("remote peer with forged loopback Host received %d: %s", response.Code, response.Body.String())
			}
		})
	}
}
