package projects

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func writeFixture(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func fixture(t *testing.T) (*Manager, string) {
	t.Helper()
	repo := t.TempDir()
	writeFixture(t, repo, "service.go", "package main\n// PaymentRetryExceeded\nfunc main() {}\n")
	writeFixture(t, repo, "config.yaml", "api_key: DO_NOT_READ_CONFIG_MARKER\n")
	writeFixture(t, repo, ".env", "TOKEN=DO_NOT_READ_ENV_MARKER")
	writeFixture(t, repo, "logs/app.log", "INFO started\nERROR PaymentRetryExceeded api_key=synthetic-sensitive-value\n")
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return m, repo
}

func TestProjectInvestigationPersistsAndDoesNotMutate(t *testing.T) {
	m, repo := fixture(t)
	before, err := os.ReadFile(filepath.Join(repo, "logs/app.log"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("unexpected mutation method")
		}
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()
	p, err := m.Create(Input{Name: "Checkout local", RepositoryPath: repo, LogPath: "logs", HealthURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	validation, err := m.Validate(context.Background(), p.ID)
	if err != nil || !validation.Valid {
		t.Fatalf("validation %#v %v", validation, err)
	}
	run, err := m.Investigate(context.Background(), p.ID, "Why did retries fail?")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "needs_attention" || len(run.Evidence) < 3 || len(run.Hypotheses) < 2 || len(run.Findings) == 0 {
		t.Fatalf("insufficient report: %#v", run)
	}
	if run.SourceChanged {
		t.Fatal("stable fixture marked changing")
	}
	b, _ := json.Marshal(run)
	for _, secret := range []string{"DO_NOT_READ_CONFIG_MARKER", "DO_NOT_READ_ENV_MARKER", "synthetic-sensitive-value"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("secret leaked: %s", secret)
		}
	}
	if !strings.Contains(string(b), "REDACTED") {
		t.Fatal("missing redaction marker")
	}
	for _, finding := range run.Findings {
		if len(finding.EvidenceIDs) == 0 {
			t.Fatal("finding lacks evidence")
		}
	}
	after, _ := os.ReadFile(filepath.Join(repo, "logs/app.log"))
	if string(before) != string(after) {
		t.Fatal("inspected log mutated")
	}
	loaded, err := m.GetInvestigation(p.ID, run.ID)
	if err != nil || loaded.ID != run.ID {
		t.Fatal("run not persisted", err)
	}
	list, err := m.ListInvestigations(p.ID)
	if err != nil || len(list) != 1 {
		t.Fatal("run list", err)
	}
	again, err := NewManager(filepath.Dir(m.dir))
	if err != nil {
		t.Fatal(err)
	}
	projects, err := again.List()
	if err != nil || len(projects) != 1 || projects[0].RepositoryPath != repo {
		t.Fatal("project persistence", err)
	}
}
func TestMissingEvidenceIsInconclusive(t *testing.T) {
	m, repo := fixture(t)
	p, err := m.Create(Input{Name: "No runtime data", RepositoryPath: repo})
	if err != nil {
		t.Fatal(err)
	}
	run, err := m.Investigate(context.Background(), p.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "inconclusive" {
		t.Fatalf("missing runtime evidence must not be healthy: %s", run.Status)
	}
	if len(run.Findings) != 0 {
		t.Fatal("fabricated findings")
	}
	if len(run.Hypotheses) != 2 {
		t.Fatal("missing competing hypotheses")
	}
}
func TestRegistrationRejectsInvalidAndDuplicate(t *testing.T) {
	m, repo := fixture(t)
	if _, err := m.Create(Input{Name: "", RepositoryPath: repo}); err == nil {
		t.Fatal("empty name accepted")
	}
	if _, err := m.Create(Input{Name: "bad", RepositoryPath: repo, LogPath: filepath.Join(t.TempDir(), "outside.log")}); err == nil {
		t.Fatal("external logs accepted")
	}
	if _, err := m.Create(Input{Name: "bad", RepositoryPath: repo, LogPath: "config.yaml"}); err == nil {
		t.Fatal("config accepted as log")
	}
	p, err := m.Create(Input{Name: "Valid", RepositoryPath: repo})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Create(Input{Name: "Duplicate", RepositoryPath: repo}); err == nil {
		t.Fatal("duplicate accepted")
	}
	for _, id := range []string{"../escape", p.ID + "/../outside", "run-123456789012345678901234"} {
		if _, err = m.Get(id); err == nil {
			t.Fatal("invalid ID accepted")
		}
	}
}
func TestConcurrentReadsAndInvestigations(t *testing.T) {
	m, repo := fixture(t)
	p, err := m.Create(Input{Name: "Concurrent", RepositoryPath: repo})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Investigate(context.Background(), p.ID, "readonly"); err != nil {
				t.Error(err)
			}
			if _, err := m.List(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	runs, err := m.ListInvestigations(p.ID)
	if err != nil || len(runs) != 4 {
		t.Fatal("lost concurrent runs", err)
	}
}
func TestBoundedLogTail(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "huge.log", strings.Repeat("INFO a benign historical event\n", 20000)+"ERROR LastFailure\n")
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	tail, partial, err := tailLog(root, "huge.log")
	if err != nil || !partial || len(tail) > maxLogBytes || !strings.Contains(tail, "LastFailure") {
		t.Fatal("tail bounds failed", err)
	}
}
func TestCancelInvestigation(t *testing.T) {
	m, repo := fixture(t)
	p, err := m.Create(Input{Name: "cancel", RepositoryPath: repo})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = m.Investigate(ctx, p.ID, ""); err == nil {
		t.Fatal("cancel ignored")
	}
}
