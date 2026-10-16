package memory

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

type Record struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Kind   string `json:"kind"`
	Text   string `json:"text"`
}

type LogGroup struct {
	Signal          string   `json:"signal"`
	Count           int      `json:"count"`
	FirstOccurrence string   `json:"first_occurrence,omitempty"`
	LastOccurrence  string   `json:"last_occurrence,omitempty"`
	Endpoint        string   `json:"endpoint,omitempty"`
	Sample          string   `json:"sample"`
	Sources         []string `json:"sources"`
	RecordIDs       []string `json:"record_ids"`
}

type Context struct {
	Text            string     `json:"text"`
	RawChars        int        `json:"raw_chars"`
	SelectedChars   int        `json:"selected_chars"`
	EstimatedTokens int        `json:"estimated_tokens"`
	Sources         []string   `json:"sources"`
	LogGroups       []LogGroup `json:"log_groups"`
}

// BuildContext enforces a Unicode character budget on Text (not bytes or
// model tokens). Sources name the retained evidence; omitted records are never
// presented as reviewed evidence. The raw records remain the caller's data.
func BuildContext(raw []Record, budget int) Context {
	result := Context{Sources: []string{}, LogGroups: []LogGroup{}}
	type item struct {
		record Record
		group  *LogGroup
		rank   int
	}
	items := []item{}
	logs := []Record{}
	for i, r := range raw {
		result.RawChars += utf8.RuneCountInString(r.Text)
		if r.ID == "" {
			r.ID = fmt.Sprintf("record-%d", i+1)
		}
		if r.Source == "" {
			r.Source = r.ID
		}
		if isLog(r.Kind) {
			logs = append(logs, r)
			continue
		}
		items = append(items, item{record: r, rank: recordRank(r)})
	}
	for _, group := range SummarizeLogs(logs) {
		g := group
		text := fmt.Sprintf("%s count=%d", g.Signal, g.Count)
		if g.FirstOccurrence != "" {
			text += " first=" + g.FirstOccurrence + " last=" + g.LastOccurrence
		}
		if g.Endpoint != "" {
			text += " endpoint=" + g.Endpoint
		}
		text += "\nSample: " + g.Sample
		text += "\nRecord references: " + strings.Join(g.RecordIDs, ", ")
		parentID := g.RecordIDs[0]
		if index := strings.LastIndex(parentID, "#"); index > 0 {
			parentID = parentID[:index]
		}
		r := Record{ID: parentID, Source: g.Sources[0], Kind: "log_summary", Text: text}
		items = append(items, item{record: r, group: &g, rank: logRank(g.Signal + " " + g.Sample)})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].rank > items[j].rank })
	if budget <= 0 {
		return result
	}
	var text strings.Builder
	remaining := budget
	for _, entry := range items {
		r := entry.record
		header := fmt.Sprintf("[%s] %s | source: %s\n", r.ID, r.Kind, r.Source)
		prefix := ""
		if text.Len() > 0 {
			prefix = "\n\n"
		}
		available := remaining - utf8.RuneCountInString(prefix+header)
		// Retain the complete reference or omit the record. Never truncate IDs.
		if available < 8 {
			continue
		}
		// A single verbose tool result cannot consume the entire context.
		perRecord := budget / 2
		if perRecord < 256 {
			perRecord = 256
		}
		if available > perRecord {
			available = perRecord
		}
		body := compressText(r.Text, available)
		chunk := prefix + header + body
		text.WriteString(chunk)
		remaining -= utf8.RuneCountInString(chunk)
		result.Sources = appendUnique(result.Sources, r.Source, 0)
		if entry.group != nil {
			result.LogGroups = append(result.LogGroups, *entry.group)
			for _, source := range entry.group.Sources {
				result.Sources = appendUnique(result.Sources, source, 0)
			}
		}
	}
	result.Text = text.String()
	result.SelectedChars = utf8.RuneCountInString(result.Text)
	// Explicitly an estimate: Latin characters ~4/token, non-ASCII ~1/token.
	ascii, nonASCII := 0, 0
	for _, r := range result.Text {
		if r < 128 {
			ascii++
		} else {
			nonASCII++
		}
	}
	result.EstimatedTokens = (ascii+3)/4 + nonASCII
	return result
}

// SummarizeLogs accepts JSON arrays, {logs:[...]}, JSON lines, and plain lines.
// Counts are exact for input entries; examples/references are bounded to keep
// summaries useful even with tens of thousands of duplicate log lines.
func SummarizeLogs(raw []Record) []LogGroup {
	groups := map[string]*LogGroup{}
	for i, record := range raw {
		if record.ID == "" {
			record.ID = fmt.Sprintf("record-%d", i+1)
		}
		if record.Source == "" {
			record.Source = record.ID
		}
		for _, sample := range logSamples(record) {
			key := logSignal(sample.message)
			g := groups[key]
			if g == nil {
				g = &LogGroup{Signal: key, Sample: clipRunes(sample.message, 240), Sources: []string{}, RecordIDs: []string{}}
				groups[key] = g
			}
			g.Count++
			if sample.timestamp != "" {
				if g.FirstOccurrence == "" || sample.timestamp < g.FirstOccurrence {
					g.FirstOccurrence = sample.timestamp
				}
				if sample.timestamp > g.LastOccurrence {
					g.LastOccurrence = sample.timestamp
				}
			}
			if sample.endpoint != "" && g.Endpoint == "" {
				g.Endpoint = sample.endpoint
			}
			g.Sources = appendUnique(g.Sources, record.Source, 3)
			g.RecordIDs = appendUnique(g.RecordIDs, sample.id, 3)
		}
	}
	result := make([]LogGroup, 0, len(groups))
	for _, g := range groups {
		result = append(result, *g)
	}
	sort.Slice(result, func(i, j int) bool {
		ri, rj := logRank(result[i].Signal+" "+result[i].Sample), logRank(result[j].Signal+" "+result[j].Sample)
		if ri != rj {
			return ri > rj
		}
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].Signal < result[j].Signal
	})
	return result
}

type logSample struct {
	id, message, timestamp, endpoint string
}

func logSamples(r Record) []logSample {
	var rows []map[string]any
	parsed := json.Unmarshal([]byte(r.Text), &rows) == nil && strings.HasPrefix(strings.TrimSpace(r.Text), "[")
	if !parsed {
		var wrapper struct {
			Logs    []map[string]any `json:"logs"`
			Entries []map[string]any `json:"entries"`
		}
		if json.Unmarshal([]byte(r.Text), &wrapper) == nil {
			rows = append(wrapper.Logs, wrapper.Entries...)
			parsed = wrapper.Logs != nil || wrapper.Entries != nil
		}
	}
	if parsed {
		result := make([]logSample, 0, len(rows))
		for i, row := range rows {
			result = append(result, sampleFromMap(row, fmt.Sprintf("%s#%d", r.ID, i+1)))
		}
		return result
	}
	lines := strings.Split(r.Text, "\n")
	result := make([]logSample, 0, len(lines))
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		id := fmt.Sprintf("%s#%d", r.ID, i+1)
		var row map[string]any
		if json.Unmarshal([]byte(line), &row) == nil && row != nil {
			result = append(result, sampleFromMap(row, id))
		} else {
			result = append(result, logSample{id: id, message: line, timestamp: timestampPattern.FindString(line), endpoint: endpointPattern.FindString(line)})
		}
	}
	return result
}

func sampleFromMap(row map[string]any, id string) logSample {
	get := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := row[key].(string); ok && value != "" {
				return value
			}
		}
		return ""
	}
	message := get("message", "error", "msg", "signal")
	if message == "" {
		b, _ := json.Marshal(row)
		message = string(b)
	}
	if level := get("level", "severity"); level != "" {
		message = level + " " + message
	}
	endpoint := get("endpoint", "path", "route")
	if fields, ok := row["fields"].(map[string]any); ok && endpoint == "" {
		endpoint, _ = fields["endpoint"].(string)
	}
	return logSample{id: id, message: message, timestamp: get("timestamp", "time", "ts"), endpoint: endpoint}
}

var errorClassPattern = regexp.MustCompile(`\b[A-Z][A-Za-z]*(?:Error|Exception|Exceeded|Timeout|Exhausted|Failure|Unavailable|OOM)\b`)
var timestampPattern = regexp.MustCompile(`(?:\d{4}-\d{2}-\d{2}T)?\d{2}:\d{2}:\d{2}(?:\.\d+)?Z?`)
var endpointPattern = regexp.MustCompile(`/[a-zA-Z][a-zA-Z0-9/_-]*`)
var numberPattern = regexp.MustCompile(`\b\d+\b`)
var uuidPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f-]{27,}\b`)

func logSignal(message string) string {
	if class := errorClassPattern.FindString(message); class != "" {
		return class
	}
	message = timestampPattern.ReplaceAllString(message, "")
	message = uuidPattern.ReplaceAllString(message, "<id>")
	message = numberPattern.ReplaceAllString(message, "<n>")
	return clipRunes(strings.Join(strings.Fields(message), " "), 120)
}

func isLog(kind string) bool {
	kind = strings.ToLower(kind)
	return kind == "log" || kind == "logs" || kind == "search_logs"
}

func logRank(text string) int {
	text = strings.ToLower(text)
	for _, signal := range []string{"error", "fatal", "exceeded", "timeout", "exhausted", "failure", "unavailable", "oom"} {
		if strings.Contains(text, signal) {
			return 90
		}
	}
	if strings.Contains(text, "warn") {
		return 50
	}
	return 10
}

func recordRank(r Record) int {
	switch strings.ToLower(r.Kind) {
	case "evidence", "metrics", "metric", "diff", "deployment", "deployments", "health":
		return 100
	case "memory", "knowledge", "runbook":
		return 60
	}
	return 40 + logRank(r.Text)/3
}

func compressText(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	if limit < 20 {
		return clipRunes(text, limit)
	}
	// Preserve error-bearing lines before verbose informational text.
	lines := strings.Split(text, "\n")
	important := []string{}
	for _, line := range lines {
		if logRank(line) >= 90 {
			important = append(important, line)
		}
	}
	if len(important) > 0 && len(lines) > 1 {
		text = strings.Join(important, "\n")
	}
	marker := "\n[…truncated…]\n"
	room := limit - utf8.RuneCountInString(marker)
	runes := []rune(text)
	if len(runes) <= room {
		return text + marker
	}
	front := (room + 1) / 2
	return string(runes[:front]) + marker + string(runes[len(runes)-(room-front):])
}

func clipRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func appendUnique(values []string, value string, limit int) []string {
	for _, item := range values {
		if item == value {
			return values
		}
	}
	if limit > 0 && len(values) >= limit {
		return values
	}
	return append(values, value)
}
