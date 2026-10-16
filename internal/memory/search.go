package memory

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

type Query struct {
	Text            string `json:"text"`
	Service         string `json:"service"`
	IncidentType    string `json:"incident_type"`
	Environment     string `json:"environment"`
	Limit           int    `json:"limit"`
	IncludeSemantic bool   `json:"include_semantic"`
	IncludeEpisodic bool   `json:"include_episodic"`
}

type Hit struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	Title        string   `json:"title"`
	Text         string   `json:"text"`
	Source       string   `json:"source"`
	Status       string   `json:"status"`
	Score        float64  `json:"score"`
	Service      string   `json:"service"`
	IncidentType string   `json:"incident_type"`
	Evidence     []string `json:"evidence,omitempty"`
}

type document struct {
	Memory
	Title string `json:"title"`
	Text  string `json:"text"`
	kind  string
}

// Search combines BM25 with a deterministic concept/hash vector, then applies
// metadata and recency boosts. It uses no hosted or trained embedding model.
// Only confirmed episodic memories are eligible. Both flags false means both
// sources; callers disabling retrieval altogether should skip Search.
func (s *Store) Search(q Query) ([]Hit, error) {
	if !q.IncludeSemantic && !q.IncludeEpisodic {
		q.IncludeSemantic, q.IncludeEpisodic = true, true
	}
	if q.Limit <= 0 {
		q.Limit = 5
	}
	if q.Limit > 100 {
		q.Limit = 100
	}
	docs := []document{}
	if q.IncludeEpisodic {
		memories, err := s.ListMemories()
		if err != nil {
			return nil, err
		}
		for _, m := range memories {
			if m.Status == "confirmed" {
				docs = append(docs, document{Memory: m, Title: m.IncidentID + ": " + m.RootCause, kind: "episodic"})
			}
		}
	}
	if q.IncludeSemantic && s.knowledgeDir != "" {
		files, err := filepath.Glob(filepath.Join(s.knowledgeDir, "*.json"))
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			b, err := os.ReadFile(file)
			if err != nil {
				return nil, err
			}
			var d document
			if err := json.Unmarshal(b, &d); err != nil {
				return nil, fmt.Errorf("knowledge %s: %w", filepath.Base(file), err)
			}
			if d.ID == "" || d.Title == "" || d.Text == "" {
				return nil, fmt.Errorf("knowledge %s requires id, title, and text", filepath.Base(file))
			}
			if d.Status != "confirmed" {
				continue
			}
			d.kind = "semantic"
			if d.Source == "" {
				d.Source = filepath.ToSlash(file)
			}
			docs = append(docs, d)
		}
	}
	// Environment is a hard filter; service/type are boosts because a dependency
	// or another service can provide a useful precedent without being the cause.
	filtered := docs[:0]
	for _, d := range docs {
		if q.Environment != "" && d.Environment != "" && d.Environment != "all" && !strings.EqualFold(q.Environment, d.Environment) {
			continue
		}
		filtered = append(filtered, d)
	}
	docs = filtered
	df := map[string]int{}
	terms := make([][]string, len(docs))
	averageLength := 0.0
	for i, d := range docs {
		terms[i] = tokenize(documentText(d))
		averageLength += float64(len(terms[i]))
		for term := range frequencies(terms[i]) {
			df[term]++
		}
	}
	if len(docs) > 0 {
		averageLength /= float64(len(docs))
	}
	qterms := tokenize(q.Text)
	qconcepts := concepts(qterms)
	qvector := vector(qconcepts)
	hits := make([]Hit, 0, len(docs))
	for i, d := range docs {
		frequency := frequencies(terms[i])
		bm25 := 0.0
		for term := range frequencies(qterms) {
			count := float64(frequency[term])
			if count == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(docs)-df[term])+0.5)/(float64(df[term])+0.5))
			bm25 += idf * (count * 2.2) / (count + 1.2*(0.25+0.75*float64(len(terms[i]))/math.Max(averageLength, 1)))
		}
		dconcepts := concepts(terms[i])
		// Require an actual concept overlap, so hash collisions alone never recall.
		if len(qterms) > 0 && bm25 == 0 && !overlap(qconcepts, dconcepts) {
			continue
		}
		score := 0.6*(bm25/(1+bm25)) + 0.25*cosine(qvector, vector(dconcepts))
		if q.Service != "" && strings.EqualFold(q.Service, d.Service) {
			score += 0.35
		}
		if q.IncidentType != "" && strings.EqualFold(q.IncidentType, d.IncidentType) {
			score += 0.25
		}
		if q.Environment != "" && strings.EqualFold(q.Environment, d.Environment) {
			score += 0.05
		}
		if date, ok := parseDate(d.Date); ok {
			ageDays := math.Max(0, time.Since(date).Hours()/24)
			score += 0.1 / (1 + ageDays/180)
		}
		hits = append(hits, Hit{ID: d.ID, Kind: d.kind, Title: d.Title, Text: documentText(d), Source: d.Source, Status: d.Status, Score: math.Round(score*10000) / 10000, Service: d.Service, IncidentType: d.IncidentType, Evidence: d.Evidence})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ID < hits[j].ID
	})
	if len(hits) > q.Limit {
		hits = hits[:q.Limit]
	}
	return hits, nil
}

func documentText(d document) string {
	return strings.Join([]string{d.Title, d.Text, "Service: " + d.Service, "Incident type: " + d.IncidentType, "Pattern: " + d.Pattern, "Signal: " + d.Signal, "Root cause: " + d.RootCause, "Diagnostic: " + d.Diagnostic, "Resolution: " + d.Resolution}, "\n")
}

func tokenize(text string) []string {
	var expanded strings.Builder
	var previous rune
	for _, char := range text {
		if unicode.IsUpper(char) && unicode.IsLower(previous) {
			expanded.WriteRune(' ')
		}
		expanded.WriteRune(unicode.ToLower(char))
		previous = char
	}
	return strings.FieldsFunc(expanded.String(), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

var synonyms = map[string]string{
	"postgres": "database", "postgresql": "database", "db": "database", "sql": "database", "数据库": "database",
	"redis": "cache", "缓存": "cache", "caching": "cache",
	"release": "deployment", "deploy": "deployment", "deployed": "deployment", "部署": "deployment", "发布": "deployment",
	"retries": "retry", "重试": "retry", "retrying": "retry",
	"latency": "slow", "timeout": "slow", "timeouts": "slow", "延迟": "slow",
	"rollback": "revert", "回滚": "revert", "reverted": "revert",
	"exhausted": "overload", "saturation": "overload", "overloaded": "overload",
	"oom": "memory", "heap": "memory", "内存": "memory",
	"outage": "failure", "unavailable": "failure", "errors": "error",
}

func concepts(tokens []string) []string {
	result := make([]string, len(tokens))
	for i, token := range tokens {
		if canonical, ok := synonyms[token]; ok {
			result[i] = canonical
		} else {
			result[i] = token
		}
	}
	return result
}

func frequencies(tokens []string) map[string]int {
	f := make(map[string]int)
	for _, token := range tokens {
		f[token]++
	}
	return f
}

func overlap(a, b []string) bool {
	f := frequencies(a)
	for _, term := range b {
		if f[term] > 0 {
			return true
		}
	}
	return false
}

func vector(tokens []string) [256]float64 {
	var v [256]float64
	for term, count := range frequencies(tokens) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(term))
		v[h.Sum32()%256] += 1 + math.Log(float64(count))
	}
	return v
}

func cosine(a, b [256]float64) float64 {
	dot, an, bn := 0.0, 0.0, 0.0
	for i := range a {
		dot += a[i] * b[i]
		an += a[i] * a[i]
		bn += b[i] * b[i]
	}
	if an == 0 || bn == 0 {
		return 0
	}
	return dot / math.Sqrt(an*bn)
}

func parseDate(text string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
		if value, err := time.Parse(layout, text); err == nil {
			return value, true
		}
	}
	return time.Time{}, false
}
