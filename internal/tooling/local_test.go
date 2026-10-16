package tooling

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func projectRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}
func localForTest(t *testing.T) *Local {
	t.Helper()
	local, err := NewLocal(projectRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	return local
}
func argsFor(scenario, incident string) map[string]any {
	return map[string]any{"scenario_id": scenario, "incident_id": incident}
}

func TestLocalEvidenceChainAndRecoveryIsolation(t *testing.T) {
	local := localForTest(t)
	ctx := context.Background()
	args := argsFor("deployment-regression", "inc-1")
	data, err := local.Call(ctx, "get_metrics", args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"error_rate":22`) {
		t.Fatalf("unexpected metrics: %s", data)
	}
	args["query"] = "PaymentRetryExceeded"
	data, err = local.Call(ctx, "search_code", args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "retry.py") {
		t.Fatalf("ripgrep did not find retry.py: %s", data)
	}
	args["query"] = "--files" // Must be a literal pattern, never an rg option.
	data, err = local.Call(ctx, "search_code", args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"matches":[]`) {
		t.Fatalf("query became a command flag: %s", data)
	}
	data, err = local.Call(ctx, "git_diff", args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "-retry_limit: 5") || !strings.Contains(string(data), "+retry_limit: 2") {
		t.Fatalf("incorrect version diff: %s", data)
	}
	args["action"] = "mitigate_pool"
	if _, err = local.Call(ctx, "apply_recovery", args); err == nil {
		t.Fatal("wrong recovery was applied")
	}
	args["action"] = "rollback"
	if _, err = local.Call(ctx, "apply_recovery", args); err != nil {
		t.Fatal(err)
	}
	if _, err = local.Call(ctx, "apply_recovery", args); err != nil {
		t.Fatalf("replay must be idempotent: %v", err)
	}
	data, err = local.Call(ctx, "get_metrics", args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"error_rate":0.8`) {
		t.Fatalf("recovery not verified: %s", data)
	}
	other, err := local.Call(ctx, "get_metrics", argsFor("deployment-regression", "inc-2"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(other), `"error_rate":0.8`) {
		t.Fatal("recovery leaked across incidents")
	}
	if _, err = local.Call(ctx, "get_metrics", argsFor("cache-failure", "inc-1")); err == nil {
		t.Fatal("incident identity was rebound to another scenario")
	}
}

func TestEachScenarioHasDistinctRecoveryAndHealth(t *testing.T) {
	local := localForTest(t)
	if len(local.Scenarios()) != 5 {
		t.Fatal("expected five incident classes")
	}
	cases := map[string]string{"deployment-regression": "rollback", "database-overload": "mitigate_pool", "cache-failure": "bypass_cache", "provider-outage": "failover_provider", "resource-regression": "restart_replica"}
	for scenario, action := range cases {
		t.Run(scenario, func(t *testing.T) {
			args := argsFor(scenario, scenario)
			for _, tool := range []string{"get_metrics", "search_logs", "get_deployments", "get_topology", "get_health"} {
				if _, err := local.Call(context.Background(), tool, args); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "deployment-regression" {
				data, err := local.Call(context.Background(), "git_diff", args)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(data), `"diff":""`) {
					t.Fatalf("unrelated scenario acquired deployment diff: %s", data)
				}
			}
			args["action"] = action
			if _, err := local.Call(context.Background(), "apply_recovery", args); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRepositoryJailAndLimits(t *testing.T) {
	local := localForTest(t)
	args := argsFor("deployment-regression", "jail")
	for _, path := range []string{"../scenarios/deployment-regression.json", "C:\\Windows\\win.ini", "config.yaml:secret", "/etc/passwd"} {
		args["path"] = path
		if _, err := local.Call(context.Background(), "read_file", args); err == nil {
			t.Errorf("accepted forbidden path %q", path)
		}
	}
	args["path"] = "retry.py"
	if _, err := local.Call(context.Background(), "read_file", args); err != nil {
		t.Fatal(err)
	}
	jail := t.TempDir()
	local.repository = jail
	if err := os.WriteFile(filepath.Join(jail, "large.txt"), make([]byte, maxToolBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	args["path"] = "large.txt"
	if _, err := local.Call(context.Background(), "read_file", args); err == nil {
		t.Fatal("oversized file accepted")
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(jail, "escape")); err == nil {
		args["path"] = "escape"
		if _, err := local.Call(context.Background(), "read_file", args); err == nil {
			t.Fatal("symlink escape accepted")
		}
	} else {
		t.Log("symlink creation unavailable on this host:", err)
	}
}

func TestCancellationAndSkills(t *testing.T) {
	local := localForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := local.Call(ctx, "get_metrics", argsFor("cache-failure", "canceled")); err == nil {
		t.Fatal("canceled call succeeded")
	}
	skills, err := LoadSkills(projectRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 7 {
		t.Fatalf("want 7 skills, got %d", len(skills))
	}
	for _, skill := range skills {
		if skill.Instructions == "" {
			t.Fatalf("missing instructions: %s", skill.ID)
		}
		if _, err := json.Marshal(skill); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSyntheticRetryReproductionExecutesFixture(t *testing.T) {
	local := localForTest(t)
	if _, err := local.Call(context.Background(), "run_reproduction", argsFor("provider-outage", "unrelated-reproduction")); err == nil {
		t.Fatal("unrelated provider incident received deployment replay as current evidence")
	}
	args := argsFor("deployment-regression", "reproduction")
	// These undeclared fields must never become executable arguments.
	args["command"] = "not-a-real-command"
	args["script"] = "outside-fixture.py"
	data, err := local.Call(context.Background(), "run_reproduction", args)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Label        string `json:"label"`
		SameWorkload bool   `json:"same_workload_for_both_versions"`
		SameCode     bool   `json:"identical_retry_code_in_both_versions"`
		Results      []struct {
			RetryLimit int            `json:"retry_limit"`
			Operations int            `json:"operations"`
			Failures   int            `json:"failures"`
			ErrorRate  float64        `json:"error_rate"`
			Exceptions map[string]int `json:"observed_exceptions"`
		} `json:"results"`
	}
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Label != "synthetic replay" || !result.SameWorkload || !result.SameCode || len(result.Results) != 2 {
		t.Fatalf("invalid replay provenance: %s", data)
	}
	before, after := result.Results[0], result.Results[1]
	if before.RetryLimit != 5 || before.Operations != 1000 || before.Failures != 8 || before.ErrorRate != 0.8 || before.Exceptions["PaymentRetryExceeded"] != 8 {
		t.Fatalf("invalid baseline replay: %s", data)
	}
	if after.RetryLimit != 2 || after.Operations != 1000 || after.Failures != 220 || after.ErrorRate != 22 || after.Exceptions["PaymentRetryExceeded"] != 220 {
		t.Fatalf("invalid regression replay: %s", data)
	}
	metrics, err := local.Call(context.Background(), "get_metrics", args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(metrics), "terminal HTTP 5xx") {
		t.Fatal("provider metric semantics missing")
	}
}

func TestSyntheticReproductionReportsInterpreterAndScriptErrors(t *testing.T) {
	local := localForTest(t)
	args := argsFor("deployment-regression", "reproduction-failed")
	interpreter, err := pythonExecutable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("INCIDENTAI_PYTHON", filepath.Join(t.TempDir(), "missing-python.exe"))
	if _, err := local.Call(context.Background(), "run_reproduction", args); err == nil || !strings.Contains(err.Error(), "INCIDENTAI_PYTHON") {
		t.Fatalf("interpreter failure not surfaced: %v", err)
	}
	t.Setenv("INCIDENTAI_PYTHON", interpreter)
	local.root = t.TempDir()
	scriptDir := filepath.Join(local.root, "simulation", "v2")
	if err := os.MkdirAll(scriptDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir, "reproduce.py"), []byte("raise RuntimeError('controlled replay failure')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Call(context.Background(), "run_reproduction", args); err == nil || !strings.Contains(err.Error(), "controlled replay failure") {
		t.Fatalf("Python stderr failure not surfaced: %v", err)
	}
}
