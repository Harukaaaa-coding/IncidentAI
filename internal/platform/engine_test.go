package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"IncidentAI/internal/memory"
	"IncidentAI/internal/tooling"
)

func testEngine(t *testing.T) (*Engine, string, string) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	local, err := tooling.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	engine, err := NewEngine(root, dataDir, local, nil, local.Scenarios())
	if err != nil {
		t.Fatal(err)
	}
	return engine, root, dataDir
}

func containsMemory(hits []memory.Hit, id string) bool {
	for _, hit := range hits {
		if hit.ID == id {
			return true
		}
	}
	return false
}

func TestFiveIncidentTypesApprovalRecoveryMemoryAndRestart(t *testing.T) {
	engine, root, dataDir := testEngine(t)
	if len(engine.Scenarios) < 5 {
		t.Fatalf("expected at least five scenario types, got %d", len(engine.Scenarios))
	}
	ctx := context.Background()
	resolvedIDs := []string{}
	for _, scenario := range engine.Scenarios {
		t.Run(scenario.ID, func(t *testing.T) {
			incident, err := engine.Create(scenario.ID, "", "multi", Options{})
			if err != nil {
				t.Fatal(err)
			}
			if incident.Engine != "offline" || incident.Status != "open" || len(incident.Hypotheses) != 0 {
				t.Fatalf("premature diagnosis or hidden model: %+v", incident)
			}
			if _, err := engine.Approve(ctx, incident.ID, "not-proposed", true, incident.Revision); !errors.Is(err, ErrConflict) {
				t.Fatalf("approval before investigation was accepted: %v", err)
			}
			investigated, err := engine.Investigate(ctx, incident.ID)
			if err != nil {
				t.Fatal(err)
			}
			if investigated.Status != "approval_required" || len(investigated.Actions) != 1 || investigated.Review.Status != "passed" {
				t.Fatalf("investigation not ready for reviewed approval: status=%s review=%+v hypotheses=%+v plan=%+v", investigated.Status, investigated.Review, investigated.Hypotheses, investigated.Plan)
			}
			if len(investigated.Hypotheses) < 2 || investigated.Hypotheses[0].Type != scenario.ID {
				t.Fatalf("wrong root cause: %+v", investigated.Hypotheses)
			}
			if investigated.Context.SelectedChars > 16000 || investigated.Context.SelectedChars == 0 {
				t.Fatalf("context budget not applied: %+v", investigated.Context)
			}
			action := investigated.Actions[0]
			for _, attempt := range []struct {
				name, action string
				approved     bool
				revision     int
			}{
				{"no affirmative approval", action.ID, false, investigated.Revision},
				{"stale revision", action.ID, true, investigated.Revision - 1},
				{"wrong action", "not-this-action", true, investigated.Revision},
			} {
				if _, err := engine.Approve(ctx, investigated.ID, attempt.action, attempt.approved, attempt.revision); err == nil {
					t.Fatalf("%s executed a recovery", attempt.name)
				}
			}
			beforeApproval, err := engine.Get(investigated.ID)
			if err != nil || beforeApproval.Status != "approval_required" || beforeApproval.Actions[0].Status != "proposed" {
				t.Fatalf("invalid approval changed state: %+v %v", beforeApproval, err)
			}
			resolved, err := engine.Approve(ctx, investigated.ID, action.ID, true, investigated.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if resolved.Status != "resolved" || resolved.Actions[0].Status != "executed" || resolved.Postmortem == nil || resolved.MemoryStatus != "candidate" || resolved.MemoryID == "" {
				t.Fatalf("resolution incomplete: %+v", resolved)
			}
			if resolved.Metrics.Points[len(resolved.Metrics.Points)-1].ErrorRate > 1 {
				t.Fatal("resolved without healthy error rate")
			}
			ids := map[string]bool{}
			for _, evidence := range resolved.Evidence {
				ids[evidence.ID] = true
			}
			if len(resolved.Postmortem.Evidence) == 0 || resolved.Postmortem.RootCause == "" || resolved.Postmortem.Resolution == "" {
				t.Fatal("postmortem lacks root cause, evidence or resolution")
			}
			for _, id := range resolved.Postmortem.Evidence {
				if !ids[id] {
					t.Fatalf("postmortem cites nonexistent evidence %s", id)
				}
			}
			if _, err := engine.Approve(ctx, resolved.ID, action.ID, true, resolved.Revision); !errors.Is(err, ErrConflict) {
				t.Fatalf("approval replay was accepted: %v", err)
			}
			query := memory.Query{Service: resolved.Service, Environment: "simulation", IncludeEpisodic: true, Limit: 100}
			hits, err := engine.Store.Search(query)
			if err != nil || containsMemory(hits, resolved.MemoryID) {
				t.Fatalf("unreviewed memory leaked into retrieval: %+v %v", hits, err)
			}
			if _, err := engine.ReviewMemory(resolved.ID, "confirmed", ""); err == nil {
				t.Fatal("memory review accepted missing human identity")
			}
			reviewed, err := engine.ReviewMemory(resolved.ID, "confirmed", "test-operator")
			if err != nil || reviewed.MemoryStatus != "confirmed" {
				t.Fatalf("memory confirmation failed: %+v %v", reviewed, err)
			}
			hits, err = engine.Store.Search(query)
			if err != nil || !containsMemory(hits, resolved.MemoryID) {
				t.Fatalf("confirmed memory not retrievable: %+v %v", hits, err)
			}
			next, err := engine.Create(scenario.ID, "repeat incident", "single", Options{})
			if err != nil {
				t.Fatal(err)
			}
			next, err = engine.Investigate(ctx, next.ID)
			if err != nil || !containsMemory(next.SimilarIncidents, resolved.MemoryID) {
				t.Fatalf("next incident did not recall verified history: %+v %v", next.SimilarIncidents, err)
			}
			if next.Status != "approval_required" {
				t.Fatalf("new incident inherited prior recovery state: %s", next.Status)
			}
			resolvedIDs = append(resolvedIDs, resolved.ID)
		})
	}
	local, err := tooling.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewEngine(root, dataDir, local, nil, local.Scenarios())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range resolvedIDs {
		incident, err := restarted.Get(id)
		if err != nil || incident.Status != "resolved" || incident.MemoryStatus != "confirmed" || incident.Postmortem == nil {
			t.Fatalf("restart lost state for %s: %+v %v", id, incident, err)
		}
		raw, err := local.Call(ctx, "get_metrics", map[string]any{"scenario_id": incident.ScenarioID, "incident_id": id})
		var metrics Metrics
		if err != nil || json.Unmarshal(raw, &metrics) != nil || len(metrics.Points) == 0 || metrics.Points[len(metrics.Points)-1].ErrorRate > 1 {
			t.Fatalf("restart did not restore resolved simulation: %s %v", raw, err)
		}
	}
}

func TestInvestigationCannotInvokeMutationsOrBypassKnowledgeOptions(t *testing.T) {
	engine, _, _ := testEngine(t)
	i, err := engine.Create("deployment-regression", "", "single", Options{NoMemory: true, NoRAG: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"apply_recovery", "rollback", "restart_replica", "database_mutation", "unknown"} {
		if _, err := engine.call(context.Background(), &i, tool, map[string]any{"action": "rollback"}); err == nil {
			t.Fatalf("investigation allowed mutation %s", tool)
		}
	}
	for _, tool := range []string{"search_knowledge", "search_memory"} {
		raw, err := engine.call(context.Background(), &i, tool, map[string]any{"query": "checkout"})
		if err == nil {
			var hits []memory.Hit
			if json.Unmarshal(raw, &hits) != nil || len(hits) > 0 {
				t.Fatalf("%s bypassed disabled knowledge source: %s", tool, raw)
			}
		}
	}
}

func TestInterruptedRecoveryNeverAutomaticallyExecutes(t *testing.T) {
	engine, root, dataDir := testEngine(t)
	i, err := engine.Create("deployment-regression", "", "single", Options{})
	if err != nil {
		t.Fatal(err)
	}
	i.Status = "executing"
	i.Actions = []Action{{ID: "uncertain", Type: "rollback", Status: "approved"}}
	if err := engine.save(&i); err != nil {
		t.Fatal(err)
	}
	local, err := tooling.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewEngine(root, dataDir, local, nil, local.Scenarios())
	if err != nil {
		t.Fatal(err)
	}
	i, err = restarted.Get(i.ID)
	if err != nil || i.Status != "needs_evidence" || i.Error == "" {
		t.Fatalf("uncertain action was not quarantined for investigation: %+v %v", i, err)
	}
	raw, err := local.Call(context.Background(), "get_metrics", map[string]any{"incident_id": i.ID, "scenario_id": i.ScenarioID})
	if err != nil || strings.Contains(string(raw), `"error_rate":0.8`) {
		t.Fatalf("startup retried uncertain recovery: %s %v", raw, err)
	}
}

func TestHTTPWriteGatesAndDirectMutationBlock(t *testing.T) {
	engine, _, _ := testEngine(t)
	handler := NewServer(engine).Handler()
	for _, tc := range []struct {
		name, origin, contentType, fetchSite, body string
		status                                     int
	}{
		{"cross origin", "http://attacker.test", "application/json", "", `{}`, http.StatusForbidden},
		{"cross site", "", "application/json", "cross-site", `{}`, http.StatusForbidden},
		{"non JSON", "", "text/plain", "", `{}`, http.StatusUnsupportedMediaType},
		{"unknown field", "http://localhost", "application/json", "same-origin", `{"scenario_id":"deployment-regression","surprise":true}`, http.StatusBadRequest},
		{"trailing JSON", "http://localhost", "application/json", "same-origin", `{"scenario_id":"deployment-regression"}{}`, http.StatusBadRequest},
		{"valid same origin", "http://localhost", "application/json", "same-origin", `{"scenario_id":"deployment-regression"}`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://localhost/api/v2/incidents", strings.NewReader(tc.body))
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Content-Type", tc.contentType)
			req.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tc.status {
				t.Fatalf("got %d, want %d: %s", response.Code, tc.status, response.Body.String())
			}
		})
	}
	i, err := engine.Create("deployment-regression", "", "single", Options{})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"incident_id": i.ID, "arguments": map[string]any{"action": "rollback"}})
	req := httptest.NewRequest(http.MethodPost, "http://localhost/api/v2/tools/apply_recovery", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code < 400 {
		t.Fatalf("direct HTTP mutation was allowed: %s", response.Body.String())
	}
}
