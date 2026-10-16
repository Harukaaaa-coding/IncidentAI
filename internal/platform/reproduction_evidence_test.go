package platform

import (
	"encoding/json"
	"testing"
)

func replayPayload() map[string]any {
	return map[string]any{
		"identical_retry_code_in_both_versions": true,
		"same_workload_for_both_versions":       true,
		"results": []map[string]any{
			{"retry_limit": 5, "operations": 1000, "failures": 10},
			{"retry_limit": 2, "operations": 1000, "failures": 220},
		},
	}
}

func replayEvidence(t *testing.T, value any) Evidence {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return Evidence{ID: "REPLAY-1", Kind: "fact", Tool: "run_reproduction", Source: "controlled-synthetic-replay", Content: raw}
}

func deploymentHypothesis(t *testing.T, observations []Evidence) Hypothesis {
	t.Helper()
	for _, hypothesis := range Diagnose(observations) {
		if hypothesis.Type == "deployment-regression" {
			return hypothesis
		}
	}
	t.Fatal("deployment-regression hypothesis missing")
	return Hypothesis{}
}

func TestReproductionRequiresControlledComparableFailureCounts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
		valid  bool
	}{
		{"valid controlled replay", func(_ map[string]any) {}, true},
		{"reversed result order", func(p map[string]any) {
			r := p["results"].([]map[string]any)
			r[0], r[1] = r[1], r[0]
		}, true},
		{"zero failures with five attempts", func(p map[string]any) { p["results"].([]map[string]any)[0]["failures"] = 0 }, true},
		{"different code", func(p map[string]any) { p["identical_retry_code_in_both_versions"] = false }, false},
		{"different workload", func(p map[string]any) { p["same_workload_for_both_versions"] = false }, false},
		{"missing code verification", func(p map[string]any) { delete(p, "identical_retry_code_in_both_versions") }, false},
		{"missing workload verification", func(p map[string]any) { delete(p, "same_workload_for_both_versions") }, false},
		{"mismatched workload sizes", func(p map[string]any) { p["results"].([]map[string]any)[1]["operations"] = 500 }, false},
		{"zero operations", func(p map[string]any) {
			for _, r := range p["results"].([]map[string]any) {
				r["operations"] = 0
			}
		}, false},
		{"negative baseline failures", func(p map[string]any) { p["results"].([]map[string]any)[0]["failures"] = -1 }, false},
		{"negative new failures", func(p map[string]any) { p["results"].([]map[string]any)[1]["failures"] = -1 }, false},
		{"failures exceed operations", func(p map[string]any) { p["results"].([]map[string]any)[1]["failures"] = 1001 }, false},
		{"equal failure counts", func(p map[string]any) { p["results"].([]map[string]any)[1]["failures"] = 10 }, false},
		{"two attempts improved results", func(p map[string]any) { p["results"].([]map[string]any)[1]["failures"] = 5 }, false},
		{"wrong baseline retry limit", func(p map[string]any) { p["results"].([]map[string]any)[0]["retry_limit"] = 4 }, false},
		{"wrong changed retry limit", func(p map[string]any) { p["results"].([]map[string]any)[1]["retry_limit"] = 3 }, false},
		{"missing baseline failure measurement", func(p map[string]any) { delete(p["results"].([]map[string]any)[0], "failures") }, false},
		{"null baseline failure measurement", func(p map[string]any) { p["results"].([]map[string]any)[0]["failures"] = nil }, false},
		{"fractional failure measurement", func(p map[string]any) { p["results"].([]map[string]any)[0]["failures"] = 1.5 }, false},
		{"one result only", func(p map[string]any) { p["results"] = p["results"].([]map[string]any)[:1] }, false},
		{"extra result", func(p map[string]any) {
			r := p["results"].([]map[string]any)
			p["results"] = append(r, r[0])
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := replayPayload()
			tc.mutate(payload)
			evidence := replayEvidence(t, payload)
			hypotheses := Diagnose([]Evidence{evidence})
			for _, hypothesis := range hypotheses {
				want := tc.valid && hypothesis.Type == "deployment-regression"
				if got := contains(hypothesis.Evidence, evidence.ID); got != want {
					t.Fatalf("replay citation=%v want=%v type=%s payload=%s", got, want, hypothesis.Type, evidence.Content)
				}
				if hypothesis.Confidence != 5 {
					t.Fatalf("replay alone raised confidence: %+v", hypothesis)
				}
			}
		})
	}
}

func TestReproductionDoesNotReplaceCurrentSignalsOrIncreaseConfidence(t *testing.T) {
	baseline := []Evidence{
		{ID: "METRIC", Kind: "fact", Tool: "get_metrics", Content: json.RawMessage(`{"signals":{"retry_limit_before":5,"retry_limit_after":2}}`)},
		{ID: "LOG", Kind: "fact", Tool: "search_logs", Content: json.RawMessage(`{"entries":[{"level":"ERROR","message":"PaymentRetryExceeded"}]}`)},
		{ID: "DEPLOY", Kind: "fact", Tool: "get_deployments", Content: json.RawMessage(`{"recent":true}`)},
		{ID: "DIFF", Kind: "fact", Tool: "git_diff", Content: json.RawMessage(`{"diff":"-retry_limit: 5\n+retry_limit: 2"}`)},
	}
	replay := replayEvidence(t, replayPayload())
	for _, missing := range []string{"", "get_metrics", "search_logs", "git_diff"} {
		name := "all current signals present"
		if missing != "" {
			name = "missing " + missing
		}
		t.Run(name, func(t *testing.T) {
			observations := []Evidence{}
			for _, observation := range baseline {
				if observation.Tool != missing {
					observations = append(observations, observation)
				}
			}
			before := deploymentHypothesis(t, observations)
			after := deploymentHypothesis(t, append(observations, replay))
			if after.Confidence != before.Confidence || !contains(after.Evidence, replay.ID) {
				t.Fatalf("replay must add citation only: before=%+v after=%+v", before, after)
			}
			if missing != "" && after.Confidence >= 75 {
				t.Fatalf("replay replaced missing current evidence %s: %+v", missing, after)
			}
		})
	}
	replay.Kind = "precedent"
	if h := deploymentHypothesis(t, []Evidence{replay}); contains(h.Evidence, replay.ID) {
		t.Fatal("historical replay was treated as a current controlled observation")
	}
	replay.Kind, replay.Content = "fact", json.RawMessage(`{"results":`)
	if h := deploymentHypothesis(t, []Evidence{replay}); contains(h.Evidence, replay.ID) {
		t.Fatal("malformed replay became supporting evidence")
	}
}
