package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"IncidentAI/internal/memory"
	"IncidentAI/internal/tooling"
)

func (e *Engine) localRecovery() bool {
	switch e.Backend.(type) {
	case *tooling.Local, *benchmarkBackend:
		return true
	default:
		return false
	}
}

func (e *Engine) Approve(ctx context.Context, id, actionID string, approved bool, revision int) (Incident, error) {
	lock := e.lock(id)
	if !lock.TryLock() {
		return Incident{}, ErrConflict
	}
	defer lock.Unlock()
	i, err := e.Get(id)
	if err != nil {
		return i, err
	}
	if !approved {
		return i, errors.New("explicit approved=true is required")
	}
	if i.Status != "approval_required" || revision != i.Revision {
		return i, ErrConflict
	}
	n := -1
	for index, a := range i.Actions {
		if a.ID == actionID {
			n = index
		}
	}
	if n < 0 {
		return i, errors.New("action is not part of this incident")
	}
	a := &i.Actions[n]
	policy := Policy(a.Type)
	if policy.Decision != "approval" || a.Decision != "approval" || a.Status != "proposed" || !e.localRecovery() {
		return i, errors.New("policy blocks this action")
	}
	if ReviewEvidence(i.Evidence, i.Hypotheses).Status != "passed" || i.Review.Status != "passed" {
		return i, errors.New("evidence review no longer supports execution")
	}
	if a.Type == "rollback" && (a.FromVersion == "" || a.ToVersion == "" || a.FromVersion == a.ToVersion) {
		return i, errors.New("rollback requires two verified distinct deployment versions")
	}
	i.Status = "executing"
	a.Status = "approved"
	e.event(&i, "human", "已批准动作 "+a.ID+"："+a.Type)
	if err = e.save(&i); err != nil {
		return i, err
	}
	raw, err := e.Backend.Call(ctx, "apply_recovery", map[string]any{"scenario_id": i.ScenarioID, "incident_id": i.ID, "action": a.Type})
	i.Usage.ToolCalls++
	if err != nil {
		a.Status = "failed"
		i.Status = "recovery_failed"
		i.Error = err.Error()
		_ = e.save(&i)
		return i, err
	}
	a.Status = "executed"
	i.Evidence = append(i.Evidence, Evidence{ID: newID("ACT-"), Kind: "action_result", Tool: "apply_recovery", Source: "simulation", Summary: "人工批准的本地模拟恢复", Agent: "recovery", Time: now(), Content: raw})
	e.event(&i, "recovery", "恢复动作已执行，开始独立读取指标验证")
	if err = e.save(&i); err != nil {
		return i, err
	}
	if err = e.collect(ctx, &i, []Step{{ID: "verify-metrics", Title: "重新读取恢复后的指标", Agent: "recovery", Tool: "get_metrics"}, {ID: "verify-health", Title: "验证依赖和资源健康", Agent: "recovery", Tool: "get_health"}, {ID: "verify-deployment", Title: "核对当前部署版本", Agent: "recovery", Tool: "get_deployments"}}); err != nil {
		return i, err
	}
	if err = verifyRecovery(i, a.Type, a.ToVersion); err != nil {
		i.Status = "recovery_failed"
		i.Error = err.Error()
		e.event(&i, "recovery", "恢复验证未通过")
		_ = e.save(&i)
		return i, err
	}
	i.Status = "resolved"
	i.Error = ""
	for index := range i.Plan {
		if i.Plan[index].ID == "recovery" {
			i.Plan[index].Status = "completed"
		}
	}
	for index := range i.Agents {
		if i.Agents[index].Name == "recovery" {
			i.Agents[index].Status = "completed"
			i.Agents[index].Summary = "Recovery verified；持续下降的错误率和依赖健康已通过复查"
		}
	}
	e.buildContext(&i)
	e.event(&i, "recovery", "Recovery verified；模拟事故已恢复")
	pm := buildPostmortem(i)
	i.Postmortem = &pm
	i.Plan = append(i.Plan, Step{ID: "postmortem", Title: "从证据与执行记录生成复盘", Agent: "commander", Tool: "write_postmortem", Status: "completed", EvidenceIDs: pm.Evidence})
	candidate, err := e.Store.SaveCandidate(memory.Memory{IncidentID: i.ID, Service: i.Service, IncidentType: i.Hypotheses[0].Type, Severity: i.Severity, Date: now(), RootCause: pm.RootCause, Resolution: pm.Resolution, Environment: "simulation", Pattern: i.Title, Signal: dominantError(i.Evidence), Diagnostic: strings.Join(i.Hypotheses[0].Evidence, ", "), Source: "incident:" + i.ID, Evidence: pm.Evidence})
	if err != nil {
		i.Error = "事故已恢复，但经验候选保存失败：" + err.Error()
		_ = e.save(&i)
		return i, err
	}
	i.MemoryID = candidate.ID
	i.MemoryStatus = candidate.Status
	e.event(&i, "knowledge", "经验候选已生成，等待人工核验后才能用于未来召回")
	return i, e.save(&i)
}

func verifyRecovery(i Incident, action, targetVersion string) error {
	var m Metrics
	var health struct {
		Signals map[string]float64 `json:"signals"`
	}
	var deployment struct {
		CurrentVersion string `json:"current_version"`
	}
	haveM, haveH, haveD := false, false, false
	for _, s := range i.Plan {
		if s.Status != "completed" || len(s.EvidenceIDs) == 0 {
			continue
		}
		for _, ev := range i.Evidence {
			if ev.ID != s.EvidenceIDs[0] {
				continue
			}
			switch s.ID {
			case "verify-metrics":
				haveM = json.Unmarshal(ev.Content, &m) == nil
			case "verify-health":
				haveH = json.Unmarshal(ev.Content, &health) == nil
			case "verify-deployment":
				haveD = json.Unmarshal(ev.Content, &deployment) == nil
			}
		}
	}
	if !haveM || !haveH || len(m.Points) < 3 {
		return errors.New("recovery verification lacks fresh metrics or health samples")
	}
	p := m.Points[len(m.Points)-3:]
	if !timeAfter(p[1].Time, p[0].Time) || !timeAfter(p[2].Time, p[1].Time) {
		return errors.New("recovery samples must have increasing timestamps")
	}
	var before string
	verifyIDs := map[string]bool{}
	for _, step := range i.Plan {
		if strings.HasPrefix(step.ID, "verify-") {
			for _, id := range step.EvidenceIDs {
				verifyIDs[id] = true
			}
		}
	}
	for _, ev := range i.Evidence {
		if ev.Tool == "get_metrics" && !verifyIDs[ev.ID] {
			var initial Metrics
			if json.Unmarshal(ev.Content, &initial) == nil && len(initial.Points) > 0 {
				latest := initial.Points[len(initial.Points)-1].Time
				if before == "" || timeAfter(latest, before) {
					before = latest
				}
			}
		}
	}
	if !timeAfter(p[0].Time, before) {
		return errors.New("recovery samples are not newer than the pre-action observation")
	}
	if p[2].ErrorRate > 1 || p[2].ErrorRate < 0 || p[0].ErrorRate < p[1].ErrorRate || p[1].ErrorRate < p[2].ErrorRate {
		return errors.New("error rate has not shown a sustained recovery")
	}
	for key, limit := range map[string]float64{"db_latency_ms": 200, "db_pool_pct": 90, "cache_error_rate": 5, "provider_error_rate": 5, "memory_pct": 90, "cpu_pct": 90} {
		value, ok := health.Signals[key]
		if !ok || value < 0 || value >= limit {
			return fmt.Errorf("missing or unhealthy post-recovery signal %s", key)
		}
	}
	if action == "rollback" && (!haveD || targetVersion == "" || deployment.CurrentVersion != targetVersion) {
		return errors.New("rollback target version has not been verified")
	}
	return nil
}

func timeAfter(current, prior string) bool {
	parse := func(value string) (time.Time, bool) {
		for _, layout := range []string{time.RFC3339Nano, "15:04:05", "15:04"} {
			if t, err := time.Parse(layout, value); err == nil {
				return t, true
			}
		}
		return time.Time{}, false
	}
	a, ok := parse(current)
	b, valid := parse(prior)
	return ok && valid && a.After(b)
}

func buildPostmortem(i Incident) Postmortem {
	h := i.Hypotheses[0]
	peak := float64(0)
	for _, p := range i.Metrics.Points {
		peak = max(peak, p.ErrorRate)
	}
	timeline := []string{}
	for _, ev := range i.Timeline {
		timeline = append(timeline, ev.Time+" · "+ev.Actor+" · "+ev.Event)
	}
	resolution := ""
	for _, a := range i.Actions {
		if a.Status == "executed" {
			resolution = a.Type + "（人工批准、本地模拟），后续错误率恢复到正常范围"
		}
	}
	return Postmortem{Summary: i.Service + " 的异常已通过受审批控制的恢复流程处理。", Impact: fmt.Sprintf("本次可见采样窗口错误率峰值 %.1f%%；请求总量未知，不能据此估算受影响用户数。", peak), Timeline: timeline, RootCause: "最可能根因：" + h.Title + "。" + h.Assessment, Evidence: h.Evidence, Resolution: resolution, PreventiveActions: []string{"将本次错误签名与依赖健康检查加入发布或运行验证。", "在分阶段部署期间检查错误率、延迟和资源趋势。", "人工核验此次经验后，再纳入长期召回；发生反例时标记过期或拒绝。"}}
}

func (e *Engine) ReviewMemory(id, status, reviewer string) (Incident, error) {
	lock := e.lock(id)
	if !lock.TryLock() {
		return Incident{}, ErrConflict
	}
	defer lock.Unlock()
	i, err := e.Get(id)
	if err != nil {
		return i, err
	}
	if i.Status != "resolved" || i.MemoryID == "" {
		return i, errors.New("only a resolved incident with a candidate memory may be reviewed")
	}
	m, err := e.Store.Review(i.MemoryID, status, reviewer)
	if err != nil {
		return i, err
	}
	i.MemoryStatus = m.Status
	e.event(&i, "human", "经验核验状态："+m.Status)
	return i, e.save(&i)
}
