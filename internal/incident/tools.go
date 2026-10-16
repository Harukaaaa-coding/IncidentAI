package incident

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Tools contains the six V1 investigation tools plus a deliberately small
// file-based knowledge search. Its file access is scoped to simulation/.
type Tools struct {
	root       string
	repository string
	knowledge  string
}

func NewTools(simulationRoot string) *Tools {
	return &Tools{
		root:       simulationRoot,
		repository: filepath.Join(simulationRoot, "repository"),
		knowledge:  filepath.Join(simulationRoot, "knowledge"),
	}
}

func (t *Tools) GetMetrics() (Metrics, error) {
	var metrics Metrics
	err := t.readJSON("metrics.json", &metrics)
	return metrics, err
}

func (t *Tools) SearchLogs(query string) ([]LogEntry, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("log query must not be empty")
	}
	var logs []LogEntry
	if err := t.readJSON("logs.json", &logs); err != nil {
		return nil, err
	}
	needle := strings.ToLower(query)
	var matches []LogEntry
	for _, entry := range logs {
		haystack := strings.ToLower(entry.Level + " " + entry.Message)
		for key, value := range entry.Fields {
			haystack += " " + strings.ToLower(key) + " " + strings.ToLower(value)
		}
		if strings.Contains(haystack, needle) {
			matches = append(matches, entry)
		}
	}
	return matches, nil
}

func (t *Tools) GetDeployments() (Deployments, error) {
	var deployments Deployments
	err := t.readJSON("deployments.json", &deployments)
	return deployments, err
}

// SearchCode executes ripgrep as requested. Output is parsed for callers,
// while ripgrep remains the actual search implementation.
func (t *Tools) SearchCode(query string) ([]CodeMatch, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("code query must not be empty")
	}
	cmd := exec.Command("rg", "-n", "--", query, ".")
	cmd.Dir = t.repository
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return []CodeMatch{}, nil
		}
		return nil, fmt.Errorf("run ripgrep: %w", err)
	}

	var matches []CodeMatch
	for _, row := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		parts := strings.SplitN(row, ":", 3)
		if len(parts) != 3 {
			continue
		}
		line, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		matches = append(matches, CodeMatch{
			Path:    normalizeCodePath(parts[0]),
			Line:    line,
			Content: parts[2],
		})
	}
	return matches, nil
}

func normalizeCodePath(path string) string {
	path = strings.TrimPrefix(path, "./")
	path = strings.TrimPrefix(path, ".\\")
	return filepath.ToSlash(path)
}

func (t *Tools) ReadFile(name string) (FileContent, error) {
	if name == "" || filepath.IsAbs(name) {
		return FileContent{}, errors.New("file path must be a repository-relative path")
	}
	clean := filepath.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return FileContent{}, errors.New("file path escapes simulation repository")
	}
	contents, err := os.ReadFile(filepath.Join(t.repository, clean))
	if err != nil {
		return FileContent{}, err
	}
	return FileContent{Path: filepath.ToSlash(clean), Content: string(contents)}, nil
}

func (t *Tools) GitDiff() (DiffResult, error) {
	// Versioned snapshots keep the V1 fixture portable in a fresh clone or ZIP;
	// no nested .git directory or locally created commit history is required.
	cmd := exec.Command("git", "diff", "--no-index", "--no-ext-diff", "--no-textconv", "--no-color", "--",
		"snapshots/v2.8.0/config.yaml", "snapshots/v2.8.1/config.yaml")
	cmd.Dir = t.root
	output, err := cmd.Output()
	if err != nil {
		// git diff --no-index returns 1 when it successfully finds differences.
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || len(output) == 0 {
			return DiffResult{}, fmt.Errorf("read simulated repository diff: %w", err)
		}
	}
	return DiffResult{FromVersion: "v2.8.0", ToVersion: "v2.8.1", Diff: string(output)}, nil
}

// SearchKnowledge is intentionally lightweight V1 RAG: it scores local
// Markdown documents by matching query tokens and returns relevant excerpts.
func (t *Tools) SearchKnowledge(query string) ([]KnowledgeResult, error) {
	terms := uniqueTerms(query)
	if len(terms) == 0 {
		return nil, errors.New("knowledge query must include a search term")
	}
	paths, err := filepath.Glob(filepath.Join(t.knowledge, "*.md"))
	if err != nil {
		return nil, err
	}
	var results []KnowledgeResult
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		lower := strings.ToLower(string(contents))
		score := 0
		for _, term := range terms {
			score += strings.Count(lower, term)
		}
		if score > 0 {
			results = append(results, KnowledgeResult{
				Path:    filepath.ToSlash(filepath.Base(path)),
				Score:   score,
				Excerpt: excerpt(string(contents), terms),
			})
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	return results, nil
}

// RollbackDeployment is deliberately a simulator. It only changes local JSON
// after the caller has used the explicit human-approval endpoint.
func (t *Tools) RollbackDeployment() (Deployments, error) {
	deployments, err := t.GetDeployments()
	if err != nil {
		return Deployments{}, err
	}
	if deployments.CurrentVersion == "v2.8.0" {
		return deployments, nil
	}
	if deployments.CurrentVersion != "v2.8.1" {
		return Deployments{}, fmt.Errorf("simulator cannot roll back unexpected version %q", deployments.CurrentVersion)
	}

	deployments.PreviousVersion = deployments.CurrentVersion
	deployments.CurrentVersion = "v2.8.0"
	deployments.DeployedAt = "14:40"
	deployments.Events = append(deployments.Events, DeploymentEvent{
		Action: "rollback", FromVersion: "v2.8.1", ToVersion: "v2.8.0", Timestamp: "14:40",
	})
	if err := t.writeJSON("deployments.json", deployments); err != nil {
		return Deployments{}, err
	}

	metrics, err := t.GetMetrics()
	if err != nil {
		return Deployments{}, err
	}
	metrics.Points = append(metrics.Points,
		MetricPoint{Time: "14:41", ErrorRate: 9},
		MetricPoint{Time: "14:44", ErrorRate: 3},
		MetricPoint{Time: "14:48", ErrorRate: 0.8},
	)
	if err := t.writeJSON("metrics.json", metrics); err != nil {
		return Deployments{}, err
	}
	return deployments, nil
}

func (t *Tools) ResetSimulation() error {
	for _, file := range []string{"metrics.json", "deployments.json"} {
		source := filepath.Join(t.root, "seed", file)
		destination := filepath.Join(t.root, file)
		contents, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if err := os.WriteFile(destination, contents, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tools) readJSON(name string, target any) error {
	contents, err := os.ReadFile(filepath.Join(t.root, name))
	if err != nil {
		return err
	}
	return json.Unmarshal(contents, target)
}

func (t *Tools) writeJSON(name string, value any) error {
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(t.root, name), append(contents, '\n'), 0o644)
}

func uniqueTerms(query string) []string {
	seen := map[string]bool{}
	var terms []string
	for _, term := range strings.Fields(strings.ToLower(query)) {
		term = strings.Trim(term, ",.;:!?()[]{}\"'")
		if len(term) >= 3 && !seen[term] {
			seen[term] = true
			terms = append(terms, term)
		}
	}
	return terms
}

func excerpt(contents string, terms []string) string {
	lower := strings.ToLower(contents)
	position := 0
	for _, term := range terms {
		if index := strings.Index(lower, term); index >= 0 {
			position = index
			break
		}
	}
	start := max(0, position-90)
	end := min(len(contents), position+260)
	return strings.TrimSpace(contents[start:end])
}
