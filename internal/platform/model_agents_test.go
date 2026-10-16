package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// This transport never opens a socket. It exercises the real model envelope,
// JSON decoding, evidence checks and reviewer gates with adversarial replies.
type scriptedModelTransport struct {
	replies []string
	finish  string
	calls   int
}

func (s *scriptedModelTransport) RoundTrip(*http.Request) (*http.Response, error) {
	if s.calls >= len(s.replies) {
		return nil, fmt.Errorf("unexpected model request %d", s.calls+1)
	}
	content := s.replies[s.calls]
	s.calls++
	finish := s.finish
	if finish == "" {
		finish = "stop"
	}
	encoded, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"message": map[string]string{"content": content}, "finish_reason": finish}},
		"usage":   map[string]int{"prompt_tokens": 100, "completion_tokens": 20},
	})
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(encoded)))}, nil
}

func installScriptedModel(engine *Engine, transport *scriptedModelTransport) {
	engine.Model = &Model{Name: "test-model", Provider: "test", Endpoint: "https://model.invalid/chat/completions", key: "test-only", client: &http.Client{Transport: transport}}
}

func investigationForModelTest(t *testing.T) (*Engine, Incident) {
	t.Helper()
	engine, _, _ := testEngine(t)
	i, err := engine.Create("deployment-regression", "", "single", Options{})
	if err != nil {
		t.Fatal(err)
	}
	i, err = engine.Investigate(context.Background(), i.ID)
	if err != nil || i.Review.Status != "passed" {
		t.Fatalf("offline evidence fixture invalid: %+v %v", i.Review, err)
	}
	return engine, i
}

func TestModelDiagnosisRequiresDistinctGroundedCitations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]Hypothesis, Incident)
		safe   bool
	}{
		{"valid evidence", func(_ []Hypothesis, _ Incident) {}, true},
		{"invented evidence", func(h []Hypothesis, _ Incident) { h[0].Evidence = []string{"OBS-made-up", h[0].Evidence[0]} }, false},
		{"duplicate evidence cannot count twice", func(h []Hypothesis, _ Incident) { h[0].Evidence = []string{h[0].Evidence[0], h[0].Evidence[0]} }, false},
		{"support presented as counterevidence", func(h []Hypothesis, _ Incident) { h[0].EvidenceAgainst = []string{h[0].Evidence[0]} }, false},
		{"unrelated current fact", func(h []Hypothesis, i Incident) {
			for _, ev := range i.Evidence {
				if ev.Tool == "get_topology" {
					h[0].Evidence = []string{h[0].Evidence[0], ev.ID}
					return
				}
			}
		}, false},
		{"unsupported confidence inflation", func(h []Hypothesis, _ Incident) { h[0], h[1] = h[1], h[0]; h[0].Confidence = 100; h[1].Confidence = 1 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine, incident := investigationForModelTest(t)
			hypotheses := append([]Hypothesis(nil), incident.Hypotheses[:2]...)
			tc.mutate(hypotheses, incident)
			body, _ := json.Marshal(map[string]any{"hypotheses": hypotheses})
			transport := &scriptedModelTransport{replies: []string{string(body), `{"sufficient":true,"findings":["Reviewed supplied citations"]}`}}
			installScriptedModel(engine, transport)
			err := engine.modelDiagnosis(context.Background(), &incident)
			approved := err == nil && incident.Review.Status == "passed"
			if approved != tc.safe {
				t.Fatalf("evidence validation: approved=%v want=%v error=%v review=%+v hypotheses=%+v", approved, tc.safe, err, incident.Review, incident.Hypotheses)
			}
			if tc.safe && incident.Usage.InputTokens != 200 {
				t.Fatalf("model usage not recorded: %+v", incident.Usage)
			}
		})
	}
}

func TestReviewerCanVetoAndModelErrorsCannotApprove(t *testing.T) {
	for _, tc := range []struct {
		name, diagnosis, reviewer, finish string
	}{
		{"reviewer veto", "valid", `{"sufficient":false,"findings":["Evidence remains ambiguous"]}`, ""},
		{"malformed diagnosis", `not JSON`, "", ""},
		{"missing diagnosis", `{}`, "", ""},
		{"truncated model reply", `{"hypotheses":[]}`, "", "length"},
		{"malformed reviewer", "valid", `[]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine, incident := investigationForModelTest(t)
			diagnosis := tc.diagnosis
			if diagnosis == "valid" {
				body, _ := json.Marshal(map[string]any{"hypotheses": incident.Hypotheses[:2]})
				diagnosis = string(body)
			}
			installScriptedModel(engine, &scriptedModelTransport{replies: []string{diagnosis, tc.reviewer}, finish: tc.finish})
			err := engine.modelDiagnosis(context.Background(), &incident)
			if err == nil && incident.Review.Status == "passed" {
				t.Fatalf("bad/vetoed model output remained approval-ready: %+v", incident.Review)
			}
		})
	}
}

func TestSpecialistCannotRequestMutation(t *testing.T) {
	engine, incident := investigationForModelTest(t)
	before := len(incident.Plan)
	installScriptedModel(engine, &scriptedModelTransport{replies: []string{`{"summary":"Rollback now","tool":"apply_recovery","arguments":{"action":"rollback"}}`}})
	if err := engine.specialists(context.Background(), &incident); err == nil {
		t.Fatal("specialist requested an executable mutation")
	}
	if len(incident.Plan) != before || incident.Usage.InputTokens != 100 {
		t.Fatalf("mutation planned or model usage lost: plan=%d usage=%+v", len(incident.Plan), incident.Usage)
	}
}
