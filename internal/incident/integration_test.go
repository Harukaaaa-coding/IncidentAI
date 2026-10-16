package incident

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitDiffWorksWithoutNestedGitRepository(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "repository"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"v2.8.0", "v2.8.1"} {
		contents, err := os.ReadFile(filepath.Join(simulationRoot(t), "snapshots", version, "config.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, "snapshots", version)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), contents, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "repository", ".git")); !os.IsNotExist(err) {
		t.Fatalf("portable fixture unexpectedly has a nested Git repository: %v", err)
	}
	diff, err := NewTools(root).GitDiff()
	if err != nil {
		t.Fatal(err)
	}
	if diff.FromVersion != "v2.8.0" || diff.ToVersion != "v2.8.1" ||
		!strings.Contains(diff.Diff, "-retry_limit: 5") || !strings.Contains(diff.Diff, "+retry_limit: 2") {
		t.Fatalf("portable diff does not contain the V1 retry regression: %#v", diff)
	}
}

func TestGitDiffRejectsMissingSnapshots(t *testing.T) {
	if _, err := NewTools(t.TempDir()).GitDiff(); err == nil {
		t.Fatal("missing snapshots must return an error, not an empty successful diff")
	}
}

func simulationRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "simulation"))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestManualToolPathFindsTheRegression(t *testing.T) {
	tools := NewTools(simulationRoot(t))

	metrics, err := tools.GetMetrics()
	if err != nil {
		t.Fatal(err)
	}
	if got := metrics.Points[len(metrics.Points)-1].ErrorRate; got != 22 {
		t.Fatalf("current error rate = %v, want 22", got)
	}

	logs, err := tools.SearchLogs("PaymentRetryExceeded")
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 3 {
		t.Fatalf("matching logs = %d, want 3", len(logs))
	}

	matches, err := tools.SearchCode("PaymentRetryExceeded")
	if err != nil {
		t.Fatal(err)
	}
	foundRetryFile := false
	for _, match := range matches {
		if match.Path == "retry.py" {
			foundRetryFile = true
		}
	}
	if !foundRetryFile {
		fatalf := "ripgrep results did not include retry.py: %#v"
		t.Fatalf(fatalf, matches)
	}

	diff, err := tools.GitDiff()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff.Diff, "-retry_limit: 5") || !strings.Contains(diff.Diff, "+retry_limit: 2") {
		t.Fatalf("diff does not contain retry regression:\n%s", diff.Diff)
	}
}

func TestCommanderProducesEvidenceBoundRollbackRecommendation(t *testing.T) {
	commander := NewCommander(NewTools(simulationRoot(t)))
	result, err := commander.Investigate(context.Background(), "checkout-service error rate suddenly rose to 22%")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "approval_required" {
		t.Fatalf("status = %q, want approval_required", result.Status)
	}
	if result.RecommendedAction == nil || !result.RecommendedAction.RequiresApproval {
		t.Fatal("rollback recommendation must require human approval")
	}
	if len(result.Hypotheses) < 2 {
		t.Fatalf("hypotheses = %d, want at least 2", len(result.Hypotheses))
	}
	if result.Hypotheses[0].Confidence != 89 || result.Hypotheses[0].ID != "H1" {
		t.Fatalf("unexpected leading hypothesis: %#v", result.Hypotheses[0])
	}
}
