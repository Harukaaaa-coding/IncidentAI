package platform

import (
	"IncidentAI/internal/memory"
	"encoding/json"
)

type Evidence struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Source  string          `json:"source"`
	Tool    string          `json:"tool"`
	Summary string          `json:"summary"`
	Content json.RawMessage `json:"content"`
	Agent   string          `json:"agent"`
	Time    string          `json:"time"`
}
type Step struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Agent       string         `json:"agent"`
	Tool        string         `json:"tool"`
	Status      string         `json:"status"`
	Error       string         `json:"error,omitempty"`
	EvidenceIDs []string       `json:"evidence_ids"`
	Arguments   map[string]any `json:"arguments,omitempty"`
}
type Hypothesis struct {
	ID              string   `json:"id"`
	Type            string   `json:"type"`
	Title           string   `json:"title"`
	Confidence      int      `json:"confidence"`
	Evidence        []string `json:"evidence"`
	EvidenceAgainst []string `json:"evidence_against"`
	Assessment      string   `json:"assessment"`
}
type Action struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Risk        string   `json:"risk"`
	Decision    string   `json:"decision"`
	Status      string   `json:"status"`
	Reason      string   `json:"reason"`
	Evidence    []string `json:"evidence"`
	FromVersion string   `json:"from_version,omitempty"`
	ToVersion   string   `json:"to_version,omitempty"`
}
type AgentStatus struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}
type Event struct {
	Time  string `json:"time"`
	Event string `json:"event"`
	Actor string `json:"actor"`
}
type Review struct {
	Status   string   `json:"status"`
	Findings []string `json:"findings"`
}
type Point struct {
	Time      string  `json:"time"`
	ErrorRate float64 `json:"error_rate"`
	Latency   float64 `json:"latency_ms"`
}
type Metrics struct {
	Service string             `json:"service"`
	Points  []Point            `json:"points"`
	Signals map[string]float64 `json:"signals"`
}
type Usage struct {
	InputTokens            int   `json:"input_tokens"`
	OutputTokens           int   `json:"output_tokens"`
	ToolCalls              int   `json:"tool_calls"`
	DurationMS             int64 `json:"duration_ms"`
	EstimatedContextTokens int   `json:"estimated_context_tokens"`
}
type Postmortem struct {
	Summary           string   `json:"summary"`
	Impact            string   `json:"impact"`
	Timeline          []string `json:"timeline"`
	RootCause         string   `json:"root_cause"`
	Evidence          []string `json:"evidence"`
	Resolution        string   `json:"resolution"`
	PreventiveActions []string `json:"preventive_actions"`
}
type Options struct {
	NoRAG     bool `json:"no_rag"`
	NoMemory  bool `json:"no_memory"`
	NoContext bool `json:"no_context"`
}
type Incident struct {
	ID               string          `json:"id"`
	Title            string          `json:"title"`
	ScenarioID       string          `json:"scenario_id"`
	Service          string          `json:"service"`
	Severity         string          `json:"severity"`
	Status           string          `json:"status"`
	Revision         int             `json:"revision"`
	CreatedAt        string          `json:"created_at"`
	UpdatedAt        string          `json:"updated_at"`
	Mode             string          `json:"mode"`
	Engine           string          `json:"engine"`
	Plan             []Step          `json:"plan"`
	Evidence         []Evidence      `json:"evidence"`
	Hypotheses       []Hypothesis    `json:"hypotheses"`
	Agents           []AgentStatus   `json:"agents"`
	Timeline         []Event         `json:"timeline"`
	Actions          []Action        `json:"actions"`
	Review           Review          `json:"review"`
	Metrics          Metrics         `json:"metrics"`
	Topology         json.RawMessage `json:"topology"`
	Context          memory.Context  `json:"context"`
	SimilarIncidents []memory.Hit    `json:"similar_incidents"`
	Postmortem       *Postmortem     `json:"postmortem,omitempty"`
	MemoryID         string          `json:"memory_id,omitempty"`
	MemoryStatus     string          `json:"memory_status,omitempty"`
	Error            string          `json:"error,omitempty"`
	Usage            Usage           `json:"usage"`
	Options          Options         `json:"options"`
}
type PolicyRule struct {
	Action   string `json:"action"`
	Risk     string `json:"risk"`
	Decision string `json:"decision"`
}

func Policy(action string) PolicyRule {
	switch action {
	case "get_metrics", "search_logs", "get_deployments", "get_topology", "get_health", "search_code", "read_file", "git_diff", "run_reproduction", "search_knowledge", "search_memory":
		return PolicyRule{action, "read", "auto"}
	case "create_incident_note":
		return PolicyRule{action, "low", "auto"}
	case "restart_replica", "mitigate_pool", "bypass_cache":
		return PolicyRule{action, "medium", "approval"}
	case "rollback", "failover_provider":
		return PolicyRule{action, "high", "approval"}
	default:
		return PolicyRule{action, "critical", "blocked"}
	}
}
