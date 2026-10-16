package platform

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"IncidentAI/internal/projects"
)

func projectRequest(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var text string
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		text = string(raw)
	}
	request := httptest.NewRequest(method, "http://localhost/api/v2"+path, strings.NewReader(text))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	return response
}
func projectFixture(t *testing.T) (*Engine, *Server, projects.Project, projects.Investigation) {
	t.Helper()
	engine, _, _ := testEngine(t)
	server := NewServer(engine)
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n// Synthetic sample only\nfunc main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("API_KEY=never-send-fixture-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	response := projectRequest(t, server, "POST", "/projects", projects.Input{Name: "Synthetic project", RepositoryPath: repo})
	if response.Code != http.StatusOK {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	var project projects.Project
	if err := json.Unmarshal(response.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	response = projectRequest(t, server, "POST", "/projects/"+project.ID+"/investigations", map[string]string{"question": "Inspect synthetic repository without any model calls"})
	if response.Code != http.StatusOK {
		t.Fatalf("investigate: %d %s", response.Code, response.Body.String())
	}
	var run projects.Investigation
	if err := json.Unmarshal(response.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if len(run.Evidence) == 0 {
		t.Fatal("expected actual local evidence")
	}
	return engine, server, project, run
}
func TestLocalProjectsDefaultFlowNeverCallsModel(t *testing.T) {
	engine, server, project, run := projectFixture(t)
	var calls atomic.Int32
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		t.Error("ordinary local inspection called model")
		w.WriteHeader(500)
	}))
	defer mock.Close()
	engine.Model = &Model{Provider: "bai", Name: "synthetic", Endpoint: mock.URL, key: "fixture", client: mock.Client()}
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/projects", nil},
		{"GET", "/projects/" + project.ID, nil},
		{"POST", "/projects/" + project.ID + "/validate", map[string]any{}},
		{"POST", "/projects/" + project.ID + "/investigations", map[string]string{"question": "Again, local only"}},
		{"GET", "/projects/" + project.ID + "/investigations", nil},
		{"GET", "/projects/" + project.ID + "/investigations/" + run.ID, nil},
	} {
		response := projectRequest(t, server, tc.method, tc.path, tc.body)
		if response.Code != 200 {
			t.Fatalf("%s: %d %s", tc.path, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "never-send-fixture-secret") {
			t.Fatal("secret file disclosed")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("model was called")
	}
}
func TestProjectModelReviewRequiresFreshExactSingleUseConsent(t *testing.T) {
	engine, server, project, run := projectFixture(t)
	var calls atomic.Int32
	var approvedPayload string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request.Messages) != 2 {
			t.Error("unexpected model request")
		} else if request.Messages[1].Content != approvedPayload {
			t.Error("model input differs from the exact approved preview")
		}
		answer, _ := json.Marshal(map[string]any{"summary": "Synthetic evidence review", "findings": []map[string]any{{"title": "Limited evidence", "detail": "No measured incident trend", "evidence_ids": []string{run.Evidence[0].ID}}}, "limitations": []string{"Synthetic test"}})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(answer)}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 8}})
	}))
	defer mock.Close()
	engine.Model = &Model{Provider: "bai", Name: "synthetic", Endpoint: mock.URL, key: "fixture", client: mock.Client()}
	base := "/projects/" + project.ID + "/investigations/" + run.ID
	for _, body := range []map[string]any{{"approved": false}, {"approved": true, "digest": "not-a-preview"}} {
		if response := projectRequest(t, server, "POST", base+"/model-review", body); response.Code < 400 {
			t.Fatal("missing consent accepted")
		}
	}
	getPreview := func() projectModelPreview {
		t.Helper()
		response := projectRequest(t, server, "POST", base+"/model-preview", map[string]any{})
		if response.Code != 200 {
			t.Fatalf("preview: %s", response.Body.String())
		}
		var p projectModelPreview
		_ = json.Unmarshal(response.Body.Bytes(), &p)
		if strings.Contains(p.Payload, "never-send-fixture-secret") {
			t.Fatal("secret in preview")
		}
		if p.Bytes != len([]byte(p.Payload)) || p.Endpoint != mock.URL {
			t.Fatal("incorrect preview")
		}
		return p
	}
	p := getPreview()
	if calls.Load() != 0 {
		t.Fatal("preview called model")
	}
	engine.Model.Endpoint = mock.URL + "/changed"
	if response := projectRequest(t, server, "POST", base+"/model-review", map[string]any{"approved": true, "digest": p.Digest}); response.Code != 409 {
		t.Fatalf("changed endpoint accepted: %d", response.Code)
	}
	engine.Model.Endpoint = mock.URL
	key := project.ID + "/" + run.ID
	server.projectMu.Lock()
	expired := server.projectPreviews[key]
	expired.issued = time.Now().Add(-11 * time.Minute)
	server.projectPreviews[key] = expired
	server.projectMu.Unlock()
	if response := projectRequest(t, server, "POST", base+"/model-review", map[string]any{"approved": true, "digest": p.Digest}); response.Code != 409 {
		t.Fatal("expired preview accepted")
	}
	p = getPreview()
	if response := projectRequest(t, NewServer(engine), "POST", base+"/model-review", map[string]any{"approved": true, "digest": p.Digest}); response.Code != 409 {
		t.Fatal("preview consent survived restart")
	}
	approvedPayload = p.Payload
	response := projectRequest(t, server, "POST", base+"/model-review", map[string]any{"approved": true, "digest": p.Digest})
	if response.Code != 200 {
		t.Fatalf("approved review: %d %s", response.Code, response.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("model calls=%d", calls.Load())
	}
	response = projectRequest(t, server, "POST", base+"/model-review", map[string]any{"approved": true, "digest": p.Digest})
	if response.Code != 409 || calls.Load() != 1 {
		t.Fatal("consent replay made another model call")
	}
	response = projectRequest(t, NewServer(engine), "GET", base+"/model-review", nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "Synthetic evidence review") {
		t.Fatal("saved review unavailable")
	}
}
func TestProjectModelRejectsInventedEvidence(t *testing.T) {
	engine, server, project, run := projectFixture(t)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer := `{"summary":"bad","findings":[{"title":"bad","detail":"invented","evidence_ids":["fake-id"]}],"limitations":[]}`
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%q},"finish_reason":"stop"}]}`, answer)
	}))
	defer mock.Close()
	engine.Model = &Model{Provider: "bai", Name: "synthetic", Endpoint: mock.URL, key: "fixture", client: mock.Client()}
	base := "/projects/" + project.ID + "/investigations/" + run.ID
	response := projectRequest(t, server, "POST", base+"/model-preview", map[string]any{})
	var p projectModelPreview
	_ = json.Unmarshal(response.Body.Bytes(), &p)
	response = projectRequest(t, server, "POST", base+"/model-review", map[string]any{"approved": true, "digest": p.Digest})
	if response.Code != 400 {
		t.Fatalf("invented evidence accepted: %d", response.Code)
	}
	if response = projectRequest(t, server, "GET", base+"/model-review", nil); response.Code != 404 {
		t.Fatal("invalid review persisted")
	}
}
