package incident

// MetricPoint is one simulated service-level metric sample.
type MetricPoint struct {
	Time      string  `json:"time"`
	ErrorRate float64 `json:"error_rate"`
}

type Metrics struct {
	Service string        `json:"service"`
	Points  []MetricPoint `json:"points"`
}

type LogEntry struct {
	Timestamp string            `json:"timestamp"`
	Level     string            `json:"level"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
}

type DeploymentEvent struct {
	Action      string `json:"action"`
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
	Timestamp   string `json:"timestamp"`
}

type Deployments struct {
	Service         string            `json:"service"`
	CurrentVersion  string            `json:"current_version"`
	PreviousVersion string            `json:"previous_version"`
	DeployedAt      string            `json:"deployed_at"`
	Events          []DeploymentEvent `json:"events"`
}

type CodeMatch struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

type FileContent struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type DiffResult struct {
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
	Diff        string `json:"diff"`
}

type KnowledgeResult struct {
	Path    string `json:"path"`
	Score   int    `json:"score"`
	Excerpt string `json:"excerpt"`
}

type Observation struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"` // fact, precedent, or recovery
	Statement string `json:"statement"`
	Source    string `json:"source"`
}

type Hypothesis struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Assessment      string   `json:"assessment"`
	Evidence        []string `json:"evidence"`
	EvidenceAgainst []string `json:"evidence_against,omitempty"`
	Confidence      int      `json:"confidence"`
}

type ToolStep struct {
	Tool    string `json:"tool"`
	Input   string `json:"input,omitempty"`
	Summary string `json:"summary"`
}

type RecommendedAction struct {
	Type             string `json:"type"`
	Service          string `json:"service"`
	FromVersion      string `json:"from_version"`
	ToVersion        string `json:"to_version"`
	Confidence       int    `json:"confidence"`
	Risk             string `json:"risk"`
	RequiresApproval bool   `json:"requires_approval"`
	Reason           string `json:"reason"`
}

type Investigation struct {
	Status               string                `json:"status"`
	Incident             string                `json:"incident"`
	Facts                []Observation         `json:"facts"`
	Hypotheses           []Hypothesis          `json:"hypotheses"`
	Steps                []ToolStep            `json:"steps"`
	Conclusion           string                `json:"conclusion"`
	RecommendedAction    *RecommendedAction    `json:"recommended_action,omitempty"`
	RecoveryVerification *RecoveryVerification `json:"recovery_verification,omitempty"`
}

type RecoveryVerification struct {
	Status    string  `json:"status"`
	ErrorRate float64 `json:"error_rate"`
	Statement string  `json:"statement"`
}

type Postmortem struct {
	Title             string   `json:"title"`
	Summary           string   `json:"summary"`
	Impact            string   `json:"impact"`
	Timeline          []string `json:"timeline"`
	RootCause         string   `json:"root_cause"`
	Evidence          []string `json:"evidence"`
	Resolution        string   `json:"resolution"`
	PreventiveActions []string `json:"preventive_actions"`
}
