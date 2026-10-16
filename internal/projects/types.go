package projects

import "time"

type Input struct {
	Name           string `json:"name"`
	RepositoryPath string `json:"repository_path"`
	LogPath        string `json:"log_path"`
	HealthURL      string `json:"health_url"`
}

type Project struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	RepositoryPath string    `json:"repository_path"`
	LogPath        string    `json:"log_path"`
	HealthURL      string    `json:"health_url"`
	CreatedAt      time.Time `json:"created_at"`
}

type Check struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}
type Validation struct {
	Valid  bool    `json:"valid"`
	Checks []Check `json:"checks"`
}
type Evidence struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Source  string `json:"source"`
	Summary string `json:"summary"`
	Content string `json:"content"`
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
}
type Finding struct {
	ID             string   `json:"id"`
	Severity       string   `json:"severity"`
	Title          string   `json:"title"`
	Detail         string   `json:"detail"`
	Recommendation string   `json:"recommendation"`
	EvidenceIDs    []string `json:"evidence_ids"`
}
type Hypothesis struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Assessment  string   `json:"assessment"`
	Status      string   `json:"status"`
	EvidenceIDs []string `json:"evidence_ids"`
}
type Investigation struct {
	ID            string       `json:"id"`
	ProjectID     string       `json:"project_id"`
	Question      string       `json:"question"`
	Status        string       `json:"status"`
	StartedAt     time.Time    `json:"started_at"`
	CompletedAt   time.Time    `json:"completed_at"`
	SourceChanged bool         `json:"source_changed"`
	Checks        []Check      `json:"checks"`
	Evidence      []Evidence   `json:"evidence"`
	Findings      []Finding    `json:"findings"`
	Hypotheses    []Hypothesis `json:"hypotheses"`
	Limitations   []string     `json:"limitations"`
}
