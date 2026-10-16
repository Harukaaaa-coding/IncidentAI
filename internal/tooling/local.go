package tooling

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxToolBytes = 256 * 1024

type incidentState struct {
	Scenario  string
	Recovered bool
}

// Local fixtures are immutable. Recovery is scoped to an incident, never shared
// through the fixture files or the V1 repository.
type Local struct {
	root       string
	repository string
	fixtures   map[string]fixture
	mu         sync.Mutex
	states     map[string]incidentState
}

func NewLocal(projectRoot string) (*Local, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, err
	}
	t := &Local{root: root, repository: filepath.Join(root, "simulation", "v2", "repository"), fixtures: map[string]fixture{}, states: map[string]incidentState{}}
	paths, err := filepath.Glob(filepath.Join(root, "simulation", "v2", "scenarios", "*.json"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, errors.New("no V2 simulation fixtures found")
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var f fixture
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("fixture %s: %w", filepath.Base(path), err)
		}
		if f.Scenario.ID == "" || len(f.Points) == 0 || len(f.Signals) == 0 || f.RecoveryAction == "" {
			return nil, fmt.Errorf("incomplete fixture %s", path)
		}
		if _, exists := t.fixtures[f.Scenario.ID]; exists {
			return nil, fmt.Errorf("duplicate scenario %q", f.Scenario.ID)
		}
		t.fixtures[f.Scenario.ID] = f
	}
	return t, nil
}

func (t *Local) Scenarios() []Scenario {
	result := make([]Scenario, 0, len(t.fixtures))
	for _, f := range t.fixtures {
		result = append(result, f.Scenario)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func definitions() []Definition {
	descriptions := map[string]string{
		"get_metrics":      "Read service error-rate and latency time series with dependency signals.",
		"search_logs":      "Search structured logs by an optional literal substring; results are bounded.",
		"get_deployments":  "Read the current and previous deployment versions and deployment time.",
		"get_topology":     "Read service dependencies and their observed health.",
		"get_health":       "Read database, cache, payment provider and resource health signals.",
		"search_code":      "Search the snapshot repository using real ripgrep JSON output and a literal query.",
		"read_file":        "Read a bounded UTF-8 text file within the snapshot repository.",
		"git_diff":         "Use real git diff to compare version snapshots for recent deployment changes.",
		"run_reproduction": "Run the fixed synthetic payment retry cohort against actual retry.py; no caller-supplied code, paths or commands.",
		"apply_recovery":   "SIMULATOR ONLY: apply the approved recovery to one incident; never exported over MCP.",
	}
	result := make([]Definition, 0, len(descriptions))
	for name, description := range descriptions {
		properties := map[string]any{"scenario_id": map[string]any{"type": "string"}, "incident_id": map[string]any{"type": "string"}, "service": map[string]any{"type": "string"}}
		required := []string{"scenario_id", "incident_id"}
		if name == "search_logs" || name == "search_code" {
			properties["query"] = map[string]any{"type": "string"}
			if name == "search_code" {
				required = append(required, "query")
			}
		}
		if name == "read_file" {
			properties["path"] = map[string]any{"type": "string"}
			required = append(required, "path")
		}
		risk := "read"
		if name == "apply_recovery" {
			properties["action"] = map[string]any{"type": "string", "enum": []string{"rollback", "mitigate_pool", "bypass_cache", "failover_provider", "restart_replica"}}
			required = append(required, "action")
			risk = "high"
		}
		result = append(result, Definition{Name: name, Description: description, Risk: risk, InputSchema: map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (t *Local) List(ctx context.Context) ([]Definition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return definitions(), nil
}
func (t *Local) Close() error { return nil }

func stringArg(args map[string]any, name string, required bool) (string, error) {
	value, exists := args[name]
	if !exists && !required {
		return "", nil
	}
	s, ok := value.(string)
	if !ok || (required && strings.TrimSpace(s) == "") || len(s) > 4096 {
		return "", fmt.Errorf("%s must be a non-empty string of at most 4096 bytes", name)
	}
	return s, nil
}

func (t *Local) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !readNames[name] && name != "apply_recovery" {
		return nil, fmt.Errorf("unknown tool %q", name)
	}
	scenarioID, err := stringArg(args, "scenario_id", true)
	if err != nil {
		return nil, err
	}
	incidentID, err := stringArg(args, "incident_id", true)
	if err != nil {
		return nil, err
	}
	f, ok := t.fixtures[scenarioID]
	if !ok {
		return nil, fmt.Errorf("unknown scenario %q", scenarioID)
	}
	t.mu.Lock()
	state, exists := t.states[incidentID]
	if exists && state.Scenario != scenarioID {
		t.mu.Unlock()
		return nil, errors.New("incident is already bound to another scenario")
	}
	if !exists {
		state = incidentState{Scenario: scenarioID}
		t.states[incidentID] = state
	}
	if name == "apply_recovery" {
		action, argErr := stringArg(args, "action", true)
		if argErr != nil {
			t.mu.Unlock()
			return nil, argErr
		}
		if action != f.RecoveryAction {
			t.mu.Unlock()
			return nil, errors.New("recovery does not address the observed fixture fault")
		}
		state.Recovered = true
		t.states[incidentID] = state
		t.mu.Unlock()
		return json.Marshal(map[string]any{"simulated": true, "incident_id": incidentID, "action": action, "applied": true})
	}
	t.mu.Unlock()
	signals := make(map[string]float64, len(f.Signals))
	for key, value := range f.Signals {
		signals[key] = value
	}
	points := append([]MetricPoint(nil), f.Points...)
	deployment := f.Deployment
	nodes := append([]Node(nil), f.Nodes...)
	if state.Recovered {
		points = append(points, MetricPoint{"14:41", 9, 320}, MetricPoint{"14:44", 3, 160}, MetricPoint{"14:48", 0.8, 95})
		for key, value := range map[string]float64{"db_latency_ms": 18, "db_pool_pct": 35, "cache_error_rate": 0, "provider_error_rate": 0.2, "memory_pct": 42, "cpu_pct": 28} {
			signals[key] = value
		}
		if f.RecoveryAction == "rollback" {
			deployment.CurrentVersion = f.Deployment.PreviousVersion
			deployment.PreviousVersion = f.Deployment.CurrentVersion
			deployment.DeployedAt = "14:40"
			signals["retry_limit_after"] = signals["retry_limit_before"]
		}
		for i := range nodes {
			nodes[i].Health = "healthy"
			nodes[i].LatencyMS = 18
		}
	}
	var value any
	switch name {
	case "get_metrics":
		value = map[string]any{"service": f.Scenario.Service, "points": points, "signals": signals, "metric_units": metricUnits()}
	case "get_health":
		value = map[string]any{"signals": signals, "metric_units": metricUnits()}
	case "get_deployments":
		value = deployment
	case "get_topology":
		value = map[string]any{"nodes": nodes, "edges": []Edge{{"frontend", "checkout-service"}, {"checkout-service", "payment-service"}, {"payment-service", "bank-api"}, {"checkout-service", "redis"}, {"checkout-service", "postgres"}}}
	case "search_logs":
		query, argErr := stringArg(args, "query", false)
		if argErr != nil {
			return nil, argErr
		}
		entries := make([]LogEntry, 0)
		total := 0
		for _, entry := range f.Logs {
			if strings.Contains(strings.ToLower(entry.Level+" "+entry.Message+" "+entry.Endpoint), strings.ToLower(query)) {
				total++
				if len(entries) < 200 {
					entries = append(entries, entry)
				}
			}
		}
		value = map[string]any{"entries": entries, "total": total, "truncated": total > len(entries)}
	case "search_code":
		query, argErr := stringArg(args, "query", true)
		if argErr != nil {
			return nil, argErr
		}
		return t.searchCode(ctx, query)
	case "read_file":
		path, argErr := stringArg(args, "path", true)
		if argErr != nil {
			return nil, argErr
		}
		return t.readFile(path)
	case "git_diff":
		diff := ""
		comparedFiles := []string{}
		if f.Deployment.Recent && f.RecoveryAction == "rollback" {
			comparedFiles = []string{"config.yaml", "retry.py", "service.py"}
			diffContext, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(diffContext, "git", "diff", "--no-index", "--no-ext-diff", "--no-color", "--", "snapshots/v2.8.0", "snapshots/v2.8.1")
			cmd.Dir = filepath.Join(t.root, "simulation", "v2")
			out, runErr := runBounded(cmd)
			var exitErr *exec.ExitError
			if runErr != nil && !(errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1) {
				return nil, fmt.Errorf("git diff: %w", runErr)
			}
			diff = string(out)
		}
		value = map[string]any{"from_version": f.Deployment.PreviousVersion, "to_version": f.Deployment.CurrentVersion, "diff": diff, "source": "git diff --no-index of immutable version snapshots", "comparison_performed": len(comparedFiles) > 0, "compared_files": comparedFiles, "coverage": "when performed: complete three-file synthetic checkout fixture snapshots; not a production repository audit"}
	case "run_reproduction":
		if scenarioID != "deployment-regression" {
			return nil, errors.New("retry reproduction is not applicable to this scenario; it only models the deployment-regression fixture")
		}
		return t.runReproduction(ctx)
	}
	return json.Marshal(value)
}

func metricUnits() map[string]string {
	return map[string]string{
		"error_rate":          "percentage points (0-100), failed checkout requests / total checkout requests",
		"latency_ms":          "milliseconds, end-to-end checkout response latency; not isolated retry-loop duration",
		"db_latency_ms":       "milliseconds, database request latency",
		"db_pool_pct":         "percentage points (0-100), occupied database connections",
		"cache_error_rate":    "percentage points (0-100), failed cache operations",
		"provider_error_rate": "percentage points (0-100), terminal HTTP 5xx responses; excludes transient per-attempt timeouts handled by retry.py",
		"memory_pct":          "percentage points (0-100), memory utilization",
		"cpu_pct":             "percentage points (0-100), CPU utilization",
		"retry_limit_before":  "maximum payment attempts per checkout request, before release",
		"retry_limit_after":   "maximum payment attempts per checkout request, after release",
	}
}

func pythonExecutable() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("INCIDENTAI_PYTHON")); configured != "" {
		path, err := exec.LookPath(configured)
		if err != nil {
			return "", fmt.Errorf("INCIDENTAI_PYTHON executable unavailable: %w", err)
		}
		return path, nil
	}
	for _, name := range []string{"python", "python3"} {
		if path, err := exec.LookPath(name); err == nil && !strings.Contains(strings.ToLower(path), "windowsapps") {
			return path, nil
		}
	}
	if userDirectory, err := os.UserHomeDir(); err == nil {
		path := filepath.Join(userDirectory, ".cache", "codex-runtimes", "codex-primary-runtime", "dependencies", "python", "python.exe")
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path, nil
		}
	}
	return "", errors.New("Python interpreter unavailable; set INCIDENTAI_PYTHON to a Python executable for the synthetic replay")
}

func (t *Local) runReproduction(ctx context.Context) (json.RawMessage, error) {
	interpreter, err := pythonExecutable()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Fixed isolated program only: no tool input is passed as code, command,
	// script path or CLI argument. -B prevents bytecode writes to the fixture.
	cmd := exec.CommandContext(ctx, interpreter, "-I", "-S", "-B", filepath.Join(t.root, "simulation", "v2", "reproduce.py"))
	cmd.Dir = filepath.Join(t.root, "simulation", "v2")
	data, err := runBounded(cmd)
	if err != nil {
		return nil, fmt.Errorf("synthetic retry reproduction: %w", err)
	}
	if !json.Valid(data) {
		return nil, errors.New("synthetic retry reproduction returned invalid JSON")
	}
	return json.RawMessage(data), nil
}

// EvalSymlinks prevents a symlink or Windows junction from escaping the jail.
func jailedPath(root, name string) (string, error) {
	if filepath.IsAbs(name) || filepath.VolumeName(name) != "" || strings.Contains(name, ":") {
		return "", errors.New("absolute paths and alternate data streams are forbidden")
	}
	clean := filepath.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes repository")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(resolvedRoot, clean))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("symlink escapes repository")
	}
	return resolved, nil
}

func (t *Local) readFile(name string) (json.RawMessage, error) {
	_, err := jailedPath(t.repository, name)
	if err != nil {
		return nil, err
	}
	// Root.Open also enforces the boundary during the actual open, closing the
	// race between checking a symlink and opening the target.
	root, err := os.OpenRoot(t.repository)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("only regular files may be read")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxToolBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxToolBytes {
		return nil, errors.New("file exceeds 256 KiB limit")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("file is not UTF-8 text")
	}
	return json.Marshal(map[string]any{"path": filepath.ToSlash(filepath.Clean(name)), "content": string(data)})
}

func (t *Local) searchCode(ctx context.Context, query string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "rg", "--json", "--fixed-strings", "--no-follow", "--max-count", "100", "--max-filesize", "256K", "--", query, ".")
	cmd.Dir = t.repository
	data, err := runBounded(cmd)
	var exitErr *exec.ExitError
	if err != nil && !(errors.As(err, &exitErr) && exitErr.ExitCode() == 1) {
		return nil, fmt.Errorf("ripgrep: %w", err)
	}
	rows := make([]map[string]any, 0)
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record struct {
			Type string `json:"type"`
			Data struct {
				Path struct {
					Text string `json:"text"`
				} `json:"path"`
				Lines struct {
					Text string `json:"text"`
				} `json:"lines"`
				Line int `json:"line_number"`
			} `json:"data"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, fmt.Errorf("invalid ripgrep JSON: %w", err)
		}
		if record.Type == "match" {
			rows = append(rows, map[string]any{"path": filepath.ToSlash(strings.TrimPrefix(strings.TrimPrefix(record.Data.Path.Text, "./"), ".\\")), "line": record.Data.Line, "content": strings.TrimRight(record.Data.Lines.Text, "\r\n")})
		}
	}
	return json.Marshal(map[string]any{"matches": rows, "query": query, "engine": "ripgrep"})
}

type boundedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buf.Len() {
		b.overflow = true
		return 0, errors.New("tool output exceeds limit")
	}
	return b.buf.Write(p)
}
func runBounded(cmd *exec.Cmd) ([]byte, error) {
	stdout := &boundedBuffer{limit: maxToolBytes}
	stderr := &boundedBuffer{limit: 8192}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if stdout.overflow || stderr.overflow {
		return nil, errors.New("tool output exceeds limit")
	}
	if err != nil && stderr.buf.Len() > 0 {
		return stdout.buf.Bytes(), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.buf.String()))
	}
	return stdout.buf.Bytes(), err
}
