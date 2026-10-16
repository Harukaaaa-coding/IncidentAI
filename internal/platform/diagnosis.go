package platform

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

var causeTitles = map[string]string{
	"deployment-regression": "部署配置回归", "database-overload": "数据库连接池过载", "cache-failure": "Redis / 缓存故障", "provider-outage": "外部支付提供商故障", "resource-regression": "内存 / CPU 资源回归",
}
var actionTypes = map[string]string{"deployment-regression": "rollback", "database-overload": "mitigate_pool", "cache-failure": "bypass_cache", "provider-outage": "failover_provider", "resource-regression": "restart_replica"}

// Diagnose is a transparent offline baseline, not an LLM impersonation or a
// calibrated probability estimator. It reads current observations only.
func Diagnose(evidence []Evidence) []Hypothesis {
	types := []string{"deployment-regression", "database-overload", "cache-failure", "provider-outage", "resource-regression"}
	hypotheses := []Hypothesis{}
	for index, kind := range types {
		h := Hypothesis{ID: fmt.Sprintf("H%d", index+1), Type: kind, Title: causeTitles[kind], Confidence: 5, Evidence: []string{}, EvidenceAgainst: []string{}, Assessment: "待验证假设；置信度为未校准的证据评分"}
		metric, signature, code, recent := false, false, false, false
		for _, ev := range evidence {
			if ev.Kind != "fact" {
				continue
			}
			support, against := false, false
			switch ev.Tool {
			case "get_metrics", "get_health":
				var d struct {
					Signals map[string]float64 `json:"signals"`
				}
				_ = json.Unmarshal(ev.Content, &d)
				switch kind {
				case "deployment-regression":
					before, b := d.Signals["retry_limit_before"]
					after, a := d.Signals["retry_limit_after"]
					support = b && a && before > after
					against = b && a && before == after
				case "database-overload":
					v, ok := d.Signals["db_latency_ms"]
					pool, pok := d.Signals["db_pool_pct"]
					support = (ok && v > 200) || (pok && pool >= 90)
					against = ok && v < 100 && pok && pool < 80
				case "cache-failure":
					v, ok := d.Signals["cache_error_rate"]
					support = ok && v >= 5
					against = ok && v < 1
				case "provider-outage":
					v, ok := d.Signals["provider_error_rate"]
					support = ok && v >= 5
					against = ok && v < 1
				case "resource-regression":
					v, ok := d.Signals["memory_pct"]
					cpu, cok := d.Signals["cpu_pct"]
					support = (ok && v >= 90) || (cok && cpu >= 90)
					against = ok && v < 80 && cok && cpu < 80
				}
				if support {
					metric = true
				}
			case "search_logs":
				needle := map[string]string{"deployment-regression": "PaymentRetryExceeded", "database-overload": "ConnectionPoolExhausted", "cache-failure": "RedisConnectionError", "provider-outage": "PaymentProviderUnavailable", "resource-regression": "MemoryPressure"}[kind]
				var logs struct {
					Entries []struct{ Level, Message string } `json:"entries"`
				}
				_ = json.Unmarshal(ev.Content, &logs)
				for _, l := range logs.Entries {
					if strings.EqualFold(l.Level, "ERROR") && strings.Contains(l.Message, needle) {
						support = true
						signature = true
					}
				}
			case "get_deployments":
				var d struct {
					Recent *bool `json:"recent"`
				}
				_ = json.Unmarshal(ev.Content, &d)
				if kind == "deployment-regression" && d.Recent != nil {
					recent = *d.Recent
					support = *d.Recent
					against = !*d.Recent
				}
			case "git_diff":
				var d struct {
					Diff *string `json:"diff"`
				}
				_ = json.Unmarshal(ev.Content, &d)
				if kind == "deployment-regression" && d.Diff != nil {
					support = strings.Contains(*d.Diff, "-retry_limit: 5") && strings.Contains(*d.Diff, "+retry_limit: 2")
					code = support
					against = strings.TrimSpace(*d.Diff) == ""
				}
			case "run_reproduction":
				// A controlled synthetic replay supports mechanism, not production
				// causation. It does not increase the baseline confidence score.
				if kind == "deployment-regression" {
					var d struct {
						IdenticalCode bool `json:"identical_retry_code_in_both_versions"`
						SameWorkload  bool `json:"same_workload_for_both_versions"`
						Results       []struct {
							RetryLimit int  `json:"retry_limit"`
							Operations int  `json:"operations"`
							Failures   *int `json:"failures"`
						} `json:"results"`
					}
					if json.Unmarshal(ev.Content, &d) == nil && d.IdenticalCode && d.SameWorkload && len(d.Results) == 2 {
						a, b := d.Results[0], d.Results[1]
						if a.RetryLimit < b.RetryLimit {
							a, b = b, a
						}
						support = a.RetryLimit == 5 && b.RetryLimit == 2 && a.Operations > 0 && a.Operations == b.Operations && a.Failures != nil && b.Failures != nil && *a.Failures >= 0 && *a.Failures < *b.Failures && *b.Failures <= b.Operations
					}
				}
			case "read_file":
				if kind == "deployment-regression" {
					var d struct {
						Content string `json:"content"`
					}
					_ = json.Unmarshal(ev.Content, &d)
					support = strings.Contains(d.Content, "PaymentRetryExceeded") && strings.Contains(d.Content, "retry_limit")
				}
			case "search_code":
				if kind == "deployment-regression" {
					var d struct {
						Matches []struct{ Content string } `json:"matches"`
					}
					_ = json.Unmarshal(ev.Content, &d)
					for _, m := range d.Matches {
						if strings.Contains(m.Content, "PaymentRetryExceeded") {
							support = true
						}
					}
				}
			case "get_topology":
				var d struct {
					Nodes []struct{ ID, Health string } `json:"nodes"`
				}
				_ = json.Unmarshal(ev.Content, &d)
				target := map[string]string{"deployment-regression": "checkout-service", "database-overload": "postgres", "cache-failure": "redis", "provider-outage": "bank-api", "resource-regression": "checkout-service"}[kind]
				for _, node := range d.Nodes {
					if node.ID == target {
						support = node.Health == "degraded" || node.Health == "unhealthy" || node.Health == "down"
						against = node.Health == "healthy"
					}
				}
			}
			if support {
				h.Evidence = append(h.Evidence, ev.ID)
			}
			if against {
				h.EvidenceAgainst = append(h.EvidenceAgainst, ev.ID)
			}
		}
		if metric {
			h.Confidence += 35
		}
		if signature {
			h.Confidence += 30
		}
		if kind == "deployment-regression" {
			if code {
				h.Confidence += 20
			}
			if recent {
				h.Confidence += 5
			}
			if !code {
				h.Confidence = min(h.Confidence, 64)
			}
		} else if metric && signature {
			h.Confidence += 20
		}
		h.Confidence = max(1, min(95, h.Confidence-len(h.EvidenceAgainst)*7))
		if h.Confidence >= 75 {
			h.Assessment = "多种当前信号支持；仍需恢复验证，历史相似性不等于当前因果"
		} else if h.Confidence < 20 {
			h.Assessment = "当前证据支持较弱或存在反证"
		}
		hypotheses = append(hypotheses, h)
	}
	sort.SliceStable(hypotheses, func(a, b int) bool { return hypotheses[a].Confidence > hypotheses[b].Confidence })
	return hypotheses
}

func ReviewEvidence(evidence []Evidence, hypotheses []Hypothesis) Review {
	r := Review{Status: "passed", Findings: []string{"部署时间相关不能单独证明因果；历史经验仅用于指导补证。", "置信度是未校准的证据评估，不是统计概率。"}}
	ids := map[string]Evidence{}
	hasMetrics, hasHealth, hasLogs := false, false, false
	for _, e := range evidence {
		ids[e.ID] = e
		if e.Tool == "get_metrics" {
			var m Metrics
			if json.Unmarshal(e.Content, &m) == nil && len(m.Points) > 0 {
				hasMetrics = true
			}
		}
		if e.Tool == "get_health" {
			var d struct {
				Signals map[string]float64 `json:"signals"`
			}
			_ = json.Unmarshal(e.Content, &d)
			hasHealth = true
			for _, key := range []string{"db_latency_ms", "db_pool_pct", "cache_error_rate", "provider_error_rate", "memory_pct", "cpu_pct"} {
				if v, ok := d.Signals[key]; !ok || v < 0 {
					hasHealth = false
				}
			}
		}
		if e.Tool == "search_logs" {
			hasLogs = true
		}
	}
	if !hasMetrics || !hasHealth || !hasLogs {
		r.Status = "needs_evidence"
		r.Findings = append(r.Findings, "关键指标、健康检查或日志缺失，不能批准恢复动作。")
	}
	if len(hypotheses) < 2 {
		r.Status = "needs_evidence"
		r.Findings = append(r.Findings, "至少需要两个竞争假设。")
		return r
	}
	if hypotheses[0].Confidence < 75 || hypotheses[0].Confidence-hypotheses[1].Confidence < 15 {
		r.Status = "needs_evidence"
		r.Findings = append(r.Findings, "当前证据不足或多个强假设冲突，需要进一步调查。")
	}
	baseline := map[string]Hypothesis{}
	for _, h := range Diagnose(evidence) {
		baseline[h.Type] = h
	}
	for _, h := range hypotheses {
		current := 0
		seen := map[string]bool{}
		sources := map[string]bool{}
		for _, id := range h.Evidence {
			if seen[id] {
				r.Status = "needs_evidence"
				r.Findings = append(r.Findings, "重复引用不能充当独立证据："+id)
				continue
			}
			seen[id] = true
			ev, ok := ids[id]
			if !ok {
				r.Status = "needs_evidence"
				r.Findings = append(r.Findings, "假设引用了不存在的证据 "+id)
			} else if ev.Kind == "fact" && !sources[ev.Source] {
				current++
				sources[ev.Source] = true
			}
		}
		for _, id := range h.EvidenceAgainst {
			if seen[id] || !contains(baseline[h.Type].EvidenceAgainst, id) {
				r.Status = "needs_evidence"
				r.Findings = append(r.Findings, "反证未被对应的当前事实支持："+id)
			}
			if _, ok := ids[id]; !ok {
				r.Status = "needs_evidence"
				r.Findings = append(r.Findings, "反证引用不存在 "+id)
			}
		}
		if h.Confidence >= 75 && current < 2 {
			r.Status = "needs_evidence"
			r.Findings = append(r.Findings, "高置信度假设缺乏至少两项当前事实。")
		}
		if h.Confidence >= 75 {
			tools := map[string]bool{}
			for _, id := range h.Evidence {
				if contains(baseline[h.Type].Evidence, id) {
					tools[ids[id].Tool] = true
				}
			}
			if !tools["search_logs"] || (!tools["get_health"] && !tools["get_metrics"]) || (h.Type == "deployment-regression" && !tools["git_diff"]) {
				r.Status = "needs_evidence"
				r.Findings = append(r.Findings, "高置信度假设未引用关键错误、健康指标或部署差异。")
			}
		}
	}
	return r
}

func (e *Engine) planRecovery(i *Incident) {
	i.Status = "needs_evidence"
	if len(i.Hypotheses) == 0 || i.Review.Status != "passed" {
		return
	}
	h := i.Hypotheses[0]
	kind, ok := actionTypes[h.Type]
	if !ok {
		return
	}
	p := Policy(kind)
	action := Action{ID: newID("act-"), Type: kind, Title: "建议执行 " + kind, Risk: p.Risk, Decision: p.Decision, Status: "proposed", Reason: h.Title + "；执行后必须重新检查指标和依赖健康。", Evidence: h.Evidence}
	if kind == "rollback" {
		for _, ev := range i.Evidence {
			if ev.Tool == "get_deployments" {
				var d struct {
					From string `json:"previous_version"`
					To   string `json:"current_version"`
				}
				_ = json.Unmarshal(ev.Content, &d)
				action.FromVersion = d.To
				action.ToVersion = d.From
			}
		}
	}
	if !e.localRecovery() {
		action.Decision = "blocked"
		action.Reason += " 当前外部 MCP 连接只开放调查读取，尚未配置受控部署执行器。"
	}
	i.Actions = []Action{action}
	if kind == "rollback" && (action.FromVersion == "" || action.ToVersion == "" || action.FromVersion == action.ToVersion) {
		i.Actions[0].Decision = "blocked"
		i.Actions[0].Reason += " 缺少可核验的当前版本和回滚目标版本。"
		action = i.Actions[0]
	}
	if action.Decision == "approval" {
		i.Status = "approval_required"
	}
	i.Plan = append(i.Plan, Step{ID: "recovery", Title: "人工批准并执行恢复", Agent: "recovery", Tool: kind, Status: "awaiting_approval", EvidenceIDs: h.Evidence})
	for n := range i.Agents {
		if i.Agents[n].Name == "recovery" {
			i.Agents[n].Status = "awaiting_approval"
			i.Agents[n].Summary = action.Reason
		}
	}
}
