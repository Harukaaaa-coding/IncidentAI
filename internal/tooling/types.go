// Package tooling provides bounded local tools and an MCP stdio adapter.
package tooling

import (
	"context"
	"encoding/json"
)

type Backend interface {
	List(context.Context) ([]Definition, error)
	Call(context.Context, string, map[string]any) (json.RawMessage, error)
	Close() error
}

type Definition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Risk        string         `json:"risk,omitempty"`
}

type Scenario struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Service     string `json:"service"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
}

type MetricPoint struct {
	Time      string  `json:"time"`
	ErrorRate float64 `json:"error_rate"`
	LatencyMS float64 `json:"latency_ms"`
}

type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	Service   string `json:"service"`
	Endpoint  string `json:"endpoint"`
}

type Deployment struct {
	CurrentVersion  string `json:"current_version"`
	PreviousVersion string `json:"previous_version"`
	DeployedAt      string `json:"deployed_at"`
	Recent          bool   `json:"recent"`
}

type Node struct {
	ID        string  `json:"id"`
	Health    string  `json:"health"`
	LatencyMS float64 `json:"latency_ms"`
}

type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type fixture struct {
	Scenario       Scenario           `json:"scenario"`
	Points         []MetricPoint      `json:"points"`
	Signals        map[string]float64 `json:"signals"`
	Logs           []LogEntry         `json:"logs"`
	Deployment     Deployment         `json:"deployment"`
	Nodes          []Node             `json:"nodes"`
	RecoveryAction string             `json:"recovery_action"`
}

var readNames = map[string]bool{
	"get_metrics": true, "search_logs": true, "get_deployments": true,
	"get_topology": true, "get_health": true, "search_code": true,
	"read_file": true, "git_diff": true, "run_reproduction": true,
}
