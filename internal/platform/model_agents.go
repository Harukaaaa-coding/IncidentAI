package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"IncidentAI/internal/memory"
)

type specialistReply struct {
	Summary   string         `json:"summary"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

func (e *Engine) instructions(role string, plan []Step) string {
	text := commanderPrinciples + "\nRole: " + role + ". Use the executable runbook as guidance."
	for _, skill := range e.Skills {
		selected := skill.ID == "incident-triage" || (role == "recovery" && skill.ID == "recovery-planning")
		for _, step := range plan {
			if strings.HasPrefix(step.ID, skill.ID+"/") {
				selected = true
			}
		}
		if selected {
			text += "\n" + skill.Title + "\n" + skill.Instructions
		}
	}
	return text
}
func addUsage(i *Incident, u Usage) {
	i.Usage.InputTokens += u.InputTokens
	i.Usage.OutputTokens += u.OutputTokens
}

func (e *Engine) specialists(ctx context.Context, i *Incident) error {
	roles := []string{"observability", "code", "knowledge"}
	if i.Mode == "single" {
		roles = []string{"commander"}
	}
	type result struct {
		role  string
		reply specialistReply
		usage Usage
		err   error
	}
	results := make([]result, len(roles))
	run := func(n int) {
		role := roles[n]
		allowed := roleTools(role)
		instruction := e.instructions(role, i.Plan) + "\nReview the gathered observations. You may request ONE additional read tool if evidence is missing, or set tool to an empty string. Return {\"summary\":\"brief evidence-bound assessment with IDs\",\"tool\":\"\",\"arguments\":{}}. Allowed tools: " + strings.Join(allowed, ", ")
		raw, usage, err := e.Model.Complete(ctx, instruction, "Incident: "+i.Title+"\nService: "+i.Service+"\n"+i.Context.Text, 3000)
		results[n] = result{role: role, usage: usage, err: err}
		if err == nil {
			results[n].err = json.Unmarshal(raw, &results[n].reply)
		}
	}
	if i.Mode == "multi" {
		var wg sync.WaitGroup
		for n := range roles {
			wg.Add(1)
			go func(n int) { defer wg.Done(); run(n) }(n)
		}
		wg.Wait()
	} else {
		run(0)
	}
	extra := []Step{}
	for _, r := range results {
		addUsage(i, r.usage)
	}
	for _, r := range results {
		if r.err != nil {
			return fmt.Errorf("%s model: %w", r.role, r.err)
		}
		for n := range i.Agents {
			if i.Agents[n].Name == r.role || r.role == "commander" {
				i.Agents[n].Summary = r.reply.Summary
			}
		}
		e.event(i, r.role, "模型完成证据检查")
		if r.reply.Tool != "" {
			if !contains(roleTools(r.role), r.reply.Tool) {
				return fmt.Errorf("%s requested disallowed tool %s", r.role, r.reply.Tool)
			}
			extra = append(extra, Step{ID: newID("model-read-"), Title: "模型补证：" + r.reply.Tool, Tool: r.reply.Tool, Agent: r.role, Arguments: r.reply.Arguments})
		}
	}
	if len(extra) > 0 {
		return e.collect(ctx, i, extra)
	}
	return e.save(i)
}
func roleTools(role string) []string {
	switch role {
	case "observability":
		return []string{"get_metrics", "search_logs", "get_health", "get_topology", "get_deployments"}
	case "code":
		return []string{"search_code", "read_file", "git_diff", "run_reproduction"}
	case "knowledge":
		return []string{"search_knowledge", "search_memory"}
	default:
		return []string{"get_metrics", "search_logs", "get_health", "get_topology", "get_deployments", "search_code", "read_file", "git_diff", "run_reproduction", "search_knowledge", "search_memory"}
	}
}
func contains(values []string, needle string) bool {
	for _, v := range values {
		if v == needle {
			return true
		}
	}
	return false
}

func (e *Engine) modelDiagnosis(ctx context.Context, i *Incident) error {
	baseline := map[string]Hypothesis{}
	for _, h := range i.Hypotheses {
		baseline[h.Type] = h
	}
	schema := `Return {"hypotheses":[{"id":"H1","type":"deployment-regression|database-overload|cache-failure|provider-outage|resource-regression","title":"...","confidence":0,"evidence":["OBS-id"],"evidence_against":[],"assessment":"..."}]}. Include at least two different competing types. Never manufacture evidence IDs. Order by plausibility. A confidence is an uncalibrated judgment. Evidence lists may be empty for unsupported alternatives. Missing data is not evidence of health.`
	schema += " Confidence must be an integer percentage from 0 to 100, for example 88, never a decimal probability. All evidence and evidence_against entries must be plain strings containing a supplied OBS ID. title and assessment must be strings. Include observed error logs and metrics in supporting evidence, and configuration diff for a deployment hypothesis."
	schema += " Cite only cause-specific positive signals as evidence, never generic symptoms for every alternative. An ID must never appear in both evidence and evidence_against of one hypothesis. Unsupported alternatives must have an empty evidence array. Use only the eligible citation sets below; their ordering is not a diagnosis. A reversible mitigation recommendation need not claim the only possible root cause."
	eligible := []map[string]any{}
	for _, kind := range []string{"deployment-regression", "database-overload", "cache-failure", "provider-outage", "resource-regression"} {
		h := baseline[kind]
		eligible = append(eligible, map[string]any{"type": kind, "eligible_support": h.Evidence, "eligible_against": h.EvidenceAgainst})
	}
	indexJSON, _ := json.Marshal(eligible)
	raw, usage, err := e.Model.Complete(ctx, e.instructions("diagnosis", i.Plan)+"\n"+schema, i.Title+"\n"+i.Context.Text+"\nEligible citations by candidate: "+string(indexJSON), 5000)
	addUsage(i, usage)
	if err != nil {
		return err
	}
	i.Evidence = append(i.Evidence, Evidence{ID: newID("MODEL-"), Kind: "model_analysis", Source: e.Model.Provider + "/" + e.Model.Name, Tool: "model_diagnosis", Summary: "模型的原始结构化诊断（推断，不是观测事实）", Content: raw, Agent: "diagnosis", Time: now()})
	var output struct {
		Hypotheses []Hypothesis `json:"hypotheses"`
	}
	if err = json.Unmarshal(raw, &output); err != nil {
		return fmt.Errorf("invalid structured diagnosis: %w", err)
	}
	if len(output.Hypotheses) < 2 || len(output.Hypotheses) > 5 {
		return errors.New("diagnosis must contain 2–5 competing hypotheses")
	}
	seen := map[string]bool{}
	for index := range output.Hypotheses {
		h := &output.Hypotheses[index]
		b, ok := baseline[h.Type]
		if !ok || seen[h.Type] {
			return errors.New("invalid or duplicate diagnosis type")
		}
		seen[h.Type] = true
		h.ID = fmt.Sprintf("H%d", index+1)
		h.Confidence = max(0, min(h.Confidence, b.Confidence))
		// A model cannot raise confidence above deterministic evidence sufficiency
		// or attach unrelated facts to a high-impact recommendation.
		for _, id := range h.Evidence {
			if !contains(b.Evidence, id) {
				h.Confidence = min(64, h.Confidence)
			}
		}
		if len(h.Evidence) == 0 {
			h.Confidence = min(20, h.Confidence)
		}
	}
	sort.SliceStable(output.Hypotheses, func(a, b int) bool { return output.Hypotheses[a].Confidence > output.Hypotheses[b].Confidence })
	i.Hypotheses = output.Hypotheses
	i.Review = ReviewEvidence(i.Evidence, i.Hypotheses)
	hJSON, _ := json.Marshal(i.Hypotheses)
	raw, usage, err = e.Model.Complete(ctx, e.instructions("reviewer", i.Plan)+`\nIndependently challenge this diagnosis for causation, alternatives, missing data, false certainty and citation quality. Return {"sufficient":true,"findings":["concise challenge with evidence IDs"]}. Set sufficient false if the recommended cause cannot support a reversible mitigation.`, string(hJSON)+"\n"+i.Context.Text, 2500)
	addUsage(i, usage)
	if err != nil {
		return err
	}
	var reviewer struct {
		Sufficient bool     `json:"sufficient"`
		Findings   []string `json:"findings"`
	}
	if err = json.Unmarshal(raw, &reviewer); err != nil {
		return errors.New("invalid reviewer response")
	}
	i.Review.Findings = append(i.Review.Findings, reviewer.Findings...)
	if !reviewer.Sufficient {
		i.Review.Status = "needs_evidence"
	}
	for n := range i.Agents {
		if i.Agents[n].Name == "diagnosis" {
			i.Agents[n].Summary = "模型输出竞争假设并通过服务端引用校验"
		}
		if i.Agents[n].Name == "reviewer" {
			i.Agents[n].Summary = strings.Join(reviewer.Findings, "；")
		}
	}
	e.event(i, "reviewer", "独立复核："+i.Review.Status)
	return e.save(i)
}

// RebuildContext is also used by offline evaluations with the same real budget.
func RebuildContext(records []memory.Record, budget int) memory.Context {
	return memory.BuildContext(records, budget)
}
