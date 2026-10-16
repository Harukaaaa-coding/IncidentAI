package platform

import (
	"IncidentAI/internal/tooling"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestRecoveryRejectsSamplesOlderThanLatestPreActionReading(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	local, err := tooling.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(root, t.TempDir(), local, nil, local.Scenarios())
	if err != nil {
		t.Fatal(err)
	}
	i, err := e.Create("deployment-regression", "freshness test", "single", Options{})
	if err != nil {
		t.Fatal(err)
	}
	i, err = e.Investigate(context.Background(), i.ID)
	if err != nil {
		t.Fatal(err)
	}
	action := i.Actions[0]
	i, err = e.Approve(context.Background(), i.ID, action.ID, true, i.Revision)
	if err != nil {
		t.Fatal(err)
	}
	late := Evidence{ID: "OBS-late", Tool: "get_metrics", Kind: "fact", Source: "get_metrics:checkout-service", Content: json.RawMessage(`{"points":[{"time":"14:43","error_rate":22}]}`)}
	i.Evidence = append(i.Evidence, late)
	if err = verifyRecovery(i, "rollback", action.ToVersion); err == nil {
		t.Fatal("stale post-action samples accepted after newer pre-action metric")
	}
}

func TestMissingHealthFieldsAreNotHealthy(t *testing.T) {
	evidence := []Evidence{{ID: "m", Source: "metrics", Tool: "get_metrics", Kind: "fact", Content: json.RawMessage(`{"points":[{"time":"14:38","error_rate":22}],"signals":{"cache_error_rate":35}}`)}, {ID: "h", Source: "health", Tool: "get_health", Kind: "fact", Content: json.RawMessage(`{"signals":{"irrelevant":1}}`)}, {ID: "l", Source: "logs", Tool: "search_logs", Kind: "fact", Content: json.RawMessage(`{"entries":[{"level":"ERROR","message":"RedisConnectionError"}]}`)}}
	review := ReviewEvidence(evidence, Diagnose(evidence))
	if review.Status != "needs_evidence" {
		t.Fatalf("incomplete health accepted: %+v", review)
	}
}
