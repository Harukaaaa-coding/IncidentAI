package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"IncidentAI/internal/memory"
	"IncidentAI/internal/projects"
	"IncidentAI/internal/tooling"
)

var ErrConflict = errors.New("incident state or revision changed; refresh before approving")

type Engine struct {
	Backend   tooling.Backend
	Store     *memory.Store
	Projects  *projects.Manager
	Skills    []tooling.Skill
	Model     *Model
	Scenarios []tooling.Scenario
	root      string
	dataDir   string
	mu        sync.Mutex
	locks     map[string]*sync.Mutex
}

func NewEngine(root, dataDir string, backend tooling.Backend, model *Model, scenarios []tooling.Scenario) (*Engine, error) {
	store, err := memory.NewStore(dataDir, filepath.Join(root, "knowledge", "v2"))
	if err != nil {
		return nil, err
	}
	skills, err := tooling.LoadSkills(root)
	if err != nil {
		return nil, err
	}
	projectManager, err := projects.NewManager(dataDir)
	if err != nil {
		return nil, err
	}
	e := &Engine{Backend: backend, Store: store, Projects: projectManager, Skills: skills, Model: model, Scenarios: scenarios, root: root, dataDir: dataDir, locks: map[string]*sync.Mutex{}}
	incidents, err := e.List()
	if err != nil {
		return nil, err
	}
	for _, i := range incidents {
		if i.Status == "investigating" || i.Status == "executing" {
			// Never retry an uncertain external action after a process interruption.
			i.Status = "needs_evidence"
			i.Error = "服务曾中断；请重新调查并核验动作结果。"
			e.event(&i, "commander", "服务恢复：中断的工作未自动重试")
			if err = e.save(&i); err != nil {
				return nil, err
			}
		}
		if _, local := backend.(*tooling.Local); local && i.Status == "resolved" {
			for _, a := range i.Actions {
				if a.Status == "executed" {
					_, err = backend.Call(context.Background(), "apply_recovery", map[string]any{"scenario_id": i.ScenarioID, "incident_id": i.ID, "action": a.Type})
					if err != nil {
						return nil, err
					}
				}
			}
		}
	}
	return e, nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b[:])
}
func (e *Engine) lock(id string) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.locks[id]
	if m == nil {
		m = &sync.Mutex{}
		e.locks[id] = m
	}
	return m
}
func (e *Engine) save(i *Incident) error {
	i.UpdatedAt = now()
	i.Revision++
	return e.Store.SaveIncident(i.ID, i)
}
func (e *Engine) event(i *Incident, actor, event string) {
	i.Timeline = append(i.Timeline, Event{now(), event, actor})
}
func (e *Engine) List() ([]Incident, error) {
	raw, err := e.Store.ListIncidents()
	if err != nil {
		return nil, err
	}
	result := []Incident{}
	for _, r := range raw {
		var i Incident
		if err = json.Unmarshal(r, &i); err != nil {
			return nil, err
		}
		result = append(result, i)
	}
	sort.Slice(result, func(a, b int) bool { return result[a].CreatedAt > result[b].CreatedAt })
	return result, nil
}
func (e *Engine) Get(id string) (Incident, error) {
	var i Incident
	r, err := e.Store.LoadIncident(id)
	if err != nil {
		return i, err
	}
	err = json.Unmarshal(r, &i)
	return i, err
}

func (e *Engine) Create(scenarioID, title, mode string, options Options) (Incident, error) {
	var s *tooling.Scenario
	for index := range e.Scenarios {
		if e.Scenarios[index].ID == scenarioID {
			s = &e.Scenarios[index]
			break
		}
	}
	if s == nil {
		return Incident{}, errors.New("unknown scenario_id")
	}
	if mode == "" {
		mode = "multi"
	}
	if mode != "multi" && mode != "single" {
		return Incident{}, errors.New("mode must be multi or single")
	}
	if title == "" {
		title = s.Name
	}
	if len([]rune(title)) > 500 {
		return Incident{}, errors.New("incident title too long")
	}
	engine := "offline"
	if e.Model != nil {
		engine = "llm"
	}
	i := Incident{ID: newID("inc-"), ScenarioID: s.ID, Title: title, Service: s.Service, Severity: s.Severity, Status: "open", Mode: mode, Engine: engine, CreatedAt: now(), Options: options, Plan: []Step{}, Evidence: []Evidence{}, Hypotheses: []Hypothesis{}, Actions: []Action{}, Agents: []AgentStatus{}, Timeline: []Event{}, SimilarIncidents: []memory.Hit{}}
	e.event(&i, "commander", "事故已建立；尚未得出根因结论")
	return i, e.save(&i)
}

func (e *Engine) call(ctx context.Context, i *Incident, name string, args map[string]any) (json.RawMessage, error) {
	if Policy(name).Decision != "auto" {
		return nil, fmt.Errorf("tool %s is not a permitted investigation read", name)
	}
	input := map[string]any{"scenario_id": i.ScenarioID, "incident_id": i.ID, "service": i.Service}
	for k, v := range args {
		if k != "incident_id" && k != "scenario_id" && k != "service" {
			input[k] = v
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if name == "search_knowledge" || name == "search_memory" {
		if (name == "search_knowledge" && i.Options.NoRAG) || (name == "search_memory" && i.Options.NoMemory) {
			return json.RawMessage(`[]`), nil
		}
		q, _ := input["query"].(string)
		hits, err := e.Store.Search(memory.Query{Text: q, Service: i.Service, Environment: "simulation", Limit: 5, IncludeSemantic: name == "search_knowledge" && !i.Options.NoRAG, IncludeEpisodic: name == "search_memory" && !i.Options.NoMemory})
		if err != nil {
			return nil, err
		}
		return json.Marshal(hits)
	}
	return e.Backend.Call(ctx, name, input)
}

func (e *Engine) runStep(ctx context.Context, i *Incident, s Step) (Step, *Evidence) {
	raw, err := e.call(ctx, i, s.Tool, s.Arguments)
	if err != nil {
		s.Status = "failed"
		s.Error = err.Error()
		return s, nil
	}
	if !json.Valid(raw) {
		s.Status = "failed"
		s.Error = "tool returned invalid JSON"
		return s, nil
	}
	s.Status = "completed"
	ev := &Evidence{ID: newID("OBS-"), Kind: "fact", Source: s.Tool + ":" + i.Service, Tool: s.Tool, Summary: s.Title, Content: raw, Agent: s.Agent, Time: now()}
	if s.Tool == "search_memory" || s.Tool == "search_knowledge" {
		ev.Kind = "precedent"
	}
	s.EvidenceIDs = []string{ev.ID}
	return s, ev
}

func (e *Engine) collect(ctx context.Context, i *Incident, steps []Step) error {
	start := len(i.Plan)
	for _, s := range steps {
		s.Status = "running"
		i.Plan = append(i.Plan, s)
	}
	if err := e.save(i); err != nil {
		return err
	}
	type result struct {
		s  Step
		ev *Evidence
	}
	results := make([]result, len(steps))
	run := func(n int) { results[n].s, results[n].ev = e.runStep(ctx, i, steps[n]) }
	if i.Mode == "multi" {
		var wg sync.WaitGroup
		for n := range steps {
			wg.Add(1)
			go func(n int) { defer wg.Done(); run(n) }(n)
		}
		wg.Wait()
	} else {
		for n := range steps {
			run(n)
		}
	}
	for n, r := range results {
		i.Plan[start+n] = r.s
		i.Usage.ToolCalls++
		if r.ev != nil {
			i.Evidence = append(i.Evidence, *r.ev)
		}
		e.event(i, r.s.Agent, r.s.Title+" · "+r.s.Status)
	}
	return e.save(i)
}

func (e *Engine) skill(id string) *tooling.Skill {
	for n := range e.Skills {
		if e.Skills[n].ID == id {
			return &e.Skills[n]
		}
	}
	return nil
}
func (e *Engine) triagePlan() []Step {
	result := []Step{}
	if skill := e.skill("incident-triage"); skill != nil {
		for _, s := range skill.Steps {
			if Policy(s.Tool).Risk == "read" {
				result = append(result, Step{ID: s.ID, Title: s.Title, Agent: s.Agent, Tool: s.Tool, Arguments: s.Arguments})
			}
		}
	}
	return result
}

func (e *Engine) Investigate(ctx context.Context, id string) (Incident, error) {
	lock := e.lock(id)
	if !lock.TryLock() {
		return Incident{}, ErrConflict
	}
	defer lock.Unlock()
	i, err := e.Get(id)
	if err != nil {
		return i, err
	}
	if i.Status == "resolved" || i.Status == "executing" {
		return i, ErrConflict
	}
	start := time.Now()
	i.Status = "investigating"
	i.Error = ""
	i.Plan = []Step{}
	i.Evidence = []Evidence{}
	i.Hypotheses = []Hypothesis{}
	i.Actions = []Action{}
	i.Usage = Usage{}
	i.Agents = []AgentStatus{{"observability", "running", "采集指标、日志、依赖健康"}, {"code", "pending", "等待错误特征"}, {"knowledge", "pending", "检索语义知识和已核验经验"}, {"diagnosis", "pending", "比较竞争假设"}, {"reviewer", "pending", "挑战相关性、缺失和冲突"}, {"recovery", "pending", "制定受策略约束的恢复建议"}}
	e.event(&i, "commander", "开始调查；模式 "+i.Mode+" / "+i.Engine)
	if err = e.save(&i); err != nil {
		return i, err
	}
	fail := func(err error) (Incident, error) {
		i.Status = "needs_evidence"
		i.Error = err.Error()
		if i.Engine == "llm" {
			for n := range i.Hypotheses {
				i.Hypotheses[n].Confidence = min(64, i.Hypotheses[n].Confidence)
				i.Hypotheses[n].Assessment = "模型调查未完成；当前候选仅用于继续补证。"
			}
		}
		i.Usage.DurationMS = time.Since(start).Milliseconds()
		saveErr := e.save(&i)
		if saveErr != nil {
			return i, saveErr
		}
		return i, err
	}
	steps := e.trageSafePlan()
	if err = e.collect(ctx, &i, steps); err != nil {
		return fail(err)
	}
	signature := dominantError(i.Evidence)
	// Select specialist steps from current observations, never hidden fixture labels.
	preliminary := Diagnose(i.Evidence)
	if len(preliminary) > 0 && preliminary[0].Confidence >= 30 {
		for _, skill := range e.Skills {
			if !contains(skill.IncidentTypes, preliminary[0].Type) {
				continue
			}
			e.event(&i, "commander", "加载调查 Skill："+skill.Title)
			for _, step := range skill.Steps {
				if Policy(step.Tool).Risk != "read" || (step.Tool == "search_knowledge" && i.Options.NoRAG) || (step.Tool == "search_memory" && i.Options.NoMemory) {
					continue
				}
				args := map[string]any{}
				for key, value := range step.Arguments {
					args[key] = value
				}
				if step.Tool == "search_code" {
					if signature == "" {
						continue
					}
					args["query"] = signature
				}
				if err = e.collect(ctx, &i, []Step{{ID: skill.ID + "/" + step.ID, Title: step.Title, Agent: step.Agent, Tool: step.Tool, Arguments: args}}); err != nil {
					return fail(err)
				}
			}
			break
		}
	}
	query := i.Service + " " + signature
	steps = []Step{{ID: "code-diff", Title: "检查当前发布差异", Agent: "code", Tool: "git_diff"}}
	if signature != "" {
		steps = append(steps, Step{ID: "code-search", Title: "定位准确错误签名：" + signature, Agent: "code", Tool: "search_code", Arguments: map[string]any{"query": signature}})
	}
	if !i.Options.NoRAG {
		steps = append(steps, Step{ID: "knowledge", Title: "检索服务 Runbook 和架构", Agent: "knowledge", Tool: "search_knowledge", Arguments: map[string]any{"query": query}})
	}
	if !i.Options.NoMemory {
		steps = append(steps, Step{ID: "memory", Title: "召回人工确认的事故经验", Agent: "knowledge", Tool: "search_memory", Arguments: map[string]any{"query": query}})
	}
	if err = e.collect(ctx, &i, steps); err != nil {
		return fail(err)
	}
	// Code location comes from tool output, not the scenario label.
	if path := matchedPath(i.Evidence); path != "" {
		if err = e.collect(ctx, &i, []Step{{ID: "code-read", Title: "阅读错误相关实现 " + path, Agent: "code", Tool: "read_file", Arguments: map[string]any{"path": path}}}); err != nil {
			return fail(err)
		}
	}
	e.buildContext(&i)
	if e.Model != nil {
		if err = e.specialists(ctx, &i); err != nil {
			return fail(err)
		}
		e.buildContext(&i)
	}
	i.Hypotheses = Diagnose(i.Evidence)
	i.Review = ReviewEvidence(i.Evidence, i.Hypotheses)
	if e.Model != nil {
		if err = e.modelDiagnosis(ctx, &i); err != nil {
			return fail(err)
		}
	}
	for n := range i.Agents {
		if i.Agents[n].Name != "recovery" {
			i.Agents[n].Status = "completed"
		}
	}
	e.planRecovery(&i)
	i.Usage.DurationMS = time.Since(start).Milliseconds()
	e.event(&i, "commander", "调查结束："+i.Status)
	return i, e.save(&i)
}

func (e *Engine) trageSafePlan() []Step {
	p := e.triagePlan()
	// A missing mandatory read cannot turn absence into a healthy assumption.
	existing := map[string]bool{}
	for _, s := range p {
		existing[s.Tool] = true
	}
	for _, tool := range []string{"get_metrics", "search_logs", "get_deployments", "get_health", "get_topology"} {
		if !existing[tool] {
			p = append(p, Step{ID: tool, Title: tool, Agent: "observability", Tool: tool})
		}
	}
	return p
}

func dominantError(evidence []Evidence) string {
	counts := map[string]int{}
	for _, ev := range evidence {
		if ev.Tool != "search_logs" {
			continue
		}
		var data struct {
			Entries []struct{ Level, Message string } `json:"entries"`
		}
		_ = json.Unmarshal(ev.Content, &data)
		for _, log := range data.Entries {
			if strings.EqualFold(log.Level, "ERROR") {
				counts[log.Message]++
			}
		}
	}
	best := ""
	for msg, n := range counts {
		if n > counts[best] || (n == counts[best] && msg < best) {
			best = msg
		}
	}
	return best
}
func matchedPath(evidence []Evidence) string {
	for _, ev := range evidence {
		if ev.Tool != "search_code" {
			continue
		}
		var d struct {
			Matches []struct{ Path string } `json:"matches"`
		}
		_ = json.Unmarshal(ev.Content, &d)
		for _, m := range d.Matches {
			if strings.HasSuffix(m.Path, "retry.py") {
				return m.Path
			}
		}
		if len(d.Matches) > 0 {
			return d.Matches[0].Path
		}
	}
	return ""
}

func (e *Engine) buildContext(i *Incident) {
	records := []memory.Record{}
	i.SimilarIncidents = []memory.Hit{}
	for _, ev := range i.Evidence {
		kind := map[string]string{"get_metrics": "metrics", "get_health": "health", "get_deployments": "deployments", "git_diff": "diff", "run_reproduction": "diff", "search_logs": "logs", "search_memory": "memory", "search_knowledge": "knowledge"}[ev.Tool]
		if kind == "" {
			kind = ev.Kind
		}
		records = append(records, memory.Record{ID: ev.ID, Source: ev.Source, Kind: kind, Text: string(ev.Content)})
		switch ev.Tool {
		case "get_metrics":
			_ = json.Unmarshal(ev.Content, &i.Metrics)
		case "get_topology":
			i.Topology = ev.Content
		case "search_memory", "search_knowledge":
			var hits []memory.Hit
			_ = json.Unmarshal(ev.Content, &hits)
			i.SimilarIncidents = append(i.SimilarIncidents, hits...)
		}
	}
	i.Context = memory.BuildContext(records, 16000)
	if i.Options.NoContext {
		var b strings.Builder
		rawChars := 0
		sources := []string{}
		for _, r := range records {
			fmt.Fprintf(&b, "[%s] %s | source: %s\n%s\n\n", r.ID, r.Kind, r.Source, r.Text)
			rawChars += len([]rune(r.Text))
			sources = append(sources, r.Source)
		}
		text := b.String()
		ascii, nonASCII := 0, 0
		for _, r := range text {
			if r < 128 {
				ascii++
			} else {
				nonASCII++
			}
		}
		i.Context = memory.Context{Text: text, RawChars: rawChars, SelectedChars: len([]rune(text)), EstimatedTokens: (ascii+3)/4 + nonASCII, Sources: sources}
	}
	i.Usage.EstimatedContextTokens = i.Context.EstimatedTokens
}
