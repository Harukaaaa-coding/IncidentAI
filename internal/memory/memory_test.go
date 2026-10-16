package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func candidate(id, service string) Memory {
	return Memory{ID: id, IncidentID: "INC-" + id, Service: service, IncidentType: "deployment-regression", Environment: "production", RootCause: "retry configuration regression", Signal: "PaymentRetryExceeded", Pattern: "retry errors after deployment", Resolution: "approved rollback with error rate recovered", Evidence: []string{"OBS-1", "DIFF-1"}, Source: "incident:INC-" + id}
}

func TestWorkingStatePersistsAndIDsCannotEscape(t *testing.T) {
	s, dir := newTestStore(t)
	if err := s.SaveIncident("INC-1", map[string]any{"status": "awaiting_approval", "evidence": []string{"OBS-1"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveIncident("../escape", 1); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err := s.LoadIncident("../escape"); err == nil {
		t.Fatal("path traversal read accepted")
	}
	restarted, err := NewStore(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := restarted.LoadIncident("INC-1")
	if err != nil || !bytes.Contains(raw, []byte("awaiting_approval")) {
		t.Fatalf("state not restored: %s, %v", raw, err)
	}
	if err := restarted.SaveIncident("INC-1", map[string]string{"status": "resolved"}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListIncidents()
	if err != nil || len(rows) != 1 || !bytes.Contains(rows[0], []byte("resolved")) {
		t.Fatalf("replacement not visible: %s, %v", rows, err)
	}
}

func TestCandidateReviewIsolationAndAppendOnlyAudit(t *testing.T) {
	s, dir := newTestStore(t)
	m := candidate("one", "checkout-service")
	m.Status, m.ReviewedBy = "confirmed", "model"
	saved, err := s.SaveCandidate(m)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != "candidate" || saved.ReviewedBy != "" {
		t.Fatal("caller bypassed human review")
	}
	q := Query{Text: "retry", IncludeEpisodic: true}
	hits, err := s.Search(q)
	if err != nil || len(hits) != 0 {
		t.Fatalf("candidate leaked: %+v %v", hits, err)
	}
	if _, err := s.Review(saved.ID, "confirmed", ""); err == nil {
		t.Fatal("reviewer identity not required")
	}
	before, err := os.ReadFile(filepath.Join(dir, "memories", "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"probable", "rejected", "confirmed", "outdated"} {
		reviewed, err := s.Review(saved.ID, status, "operator@example.test")
		if err != nil || reviewed.ReviewedAt == "" || reviewed.Source != m.Source {
			t.Fatalf("review lost provenance: %+v %v", reviewed, err)
		}
		hits, err = s.Search(q)
		if err != nil {
			t.Fatal(err)
		}
		if (len(hits) == 1) != (status == "confirmed") {
			t.Fatalf("status %s retrieval eligibility incorrect: %+v", status, hits)
		}
	}
	after, err := os.ReadFile(filepath.Join(dir, "memories", "audit.jsonl"))
	if err != nil || !bytes.HasPrefix(after, before) || bytes.Count(after, []byte("\n")) != 5 {
		t.Fatalf("audit not append-only: %v", err)
	}
	restarted, err := NewStore(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	memories, err := restarted.ListMemories()
	if err != nil || len(memories) != 1 || memories[0].Status != "outdated" || memories[0].ReviewedBy != "operator@example.test" {
		t.Fatalf("review did not persist: %+v %v", memories, err)
	}
	if _, err := s.SaveCandidate(m); err == nil {
		t.Fatal("duplicate candidate overwrote reviewed experience")
	}
}

func TestConfirmationNeedsSupportingEvidenceAndResolution(t *testing.T) {
	s, _ := newTestStore(t)
	m := candidate("unsupported", "checkout-service")
	m.Evidence = nil
	if _, err := s.SaveCandidate(m); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Review(m.ID, "confirmed", "operator"); err == nil {
		t.Fatal("unsupported memory was confirmed")
	}
}

func TestConcurrentStoresDoNotLoseWrites(t *testing.T) {
	s, dir := newTestStore(t)
	second, err := NewStore(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := s
			if i%2 == 0 {
				store = second
			}
			id := fmt.Sprintf("INC-%d", i)
			if err := store.SaveIncident(id, map[string]any{"id": id, "payload": strings.Repeat("事故", 500)}); err != nil {
				t.Error(err)
			}
			if err := store.SaveIncident("shared", map[string]int{"writer": i}); err != nil {
				t.Error(err)
			}
			if _, err := store.SaveCandidate(candidate(fmt.Sprintf("MEM-%d", i), "checkout-service")); err != nil {
				t.Error(err)
			}
			if raw, err := store.LoadIncident("shared"); err != nil || !json.Valid(raw) {
				t.Errorf("partial state observed: %s %v", raw, err)
			}
		}(i)
	}
	wg.Wait()
	memories, err := s.ListMemories()
	if err != nil || len(memories) != 24 {
		t.Fatalf("lost memory writes: %d %v", len(memories), err)
	}
	incidents, err := second.ListIncidents()
	if err != nil || len(incidents) != 25 {
		t.Fatalf("lost incident writes: %d %v", len(incidents), err)
	}
}

func TestCorruptAuditFailsClosed(t *testing.T) {
	s, dir := newTestStore(t)
	if _, err := s.SaveCandidate(candidate("first", "checkout-service")); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "memories", "audit.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"incomplete":`)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(dir, ""); err == nil {
		t.Fatal("corrupt journal silently accepted")
	}
	if _, err := s.Search(Query{IncludeEpisodic: true}); err == nil {
		t.Fatal("corrupt journal silently used for retrieval")
	}
}

func TestRetrievalRanksMetadataAndRecencyAndFiltersEnvironment(t *testing.T) {
	s, _ := newTestStore(t)
	for _, setup := range []struct{ id, service, date, environment string }{
		{"recent", "checkout-service", time.Now().UTC().Format(time.RFC3339), "production"},
		{"old", "checkout-service", "2020-01-01", "production"},
		{"other-service", "inventory-service", time.Now().UTC().Format(time.RFC3339), "production"},
		{"wrong-environment", "checkout-service", time.Now().UTC().Format(time.RFC3339), "staging"},
	} {
		m := candidate(setup.id, setup.service)
		m.Date, m.Environment = setup.date, setup.environment
		if _, err := s.SaveCandidate(m); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Review(m.ID, "confirmed", "operator"); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := s.Search(Query{Text: "retry deployment", Service: "checkout-service", IncidentType: "deployment-regression", Environment: "production", IncludeEpisodic: true, Limit: 10})
	if err != nil || len(hits) != 3 || hits[0].ID != "recent" || hits[1].ID != "old" {
		t.Fatalf("metadata/recency ranking wrong: %+v %v", hits, err)
	}
	if hits[0].Source != "incident:INC-recent" || len(hits[0].Evidence) != 2 {
		t.Fatal("retrieval lost provenance")
	}
}

func TestSemanticCorpusAndConceptSearch(t *testing.T) {
	s, err := NewStore(t.TempDir(), filepath.Join("..", "..", "knowledge", "v2"))
	if err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search(Query{Text: "PostgreSQL connections", Service: "checkout-service", IncidentType: "database-overload", Environment: "simulation", IncludeSemantic: true})
	if err != nil || len(hits) == 0 || hits[0].ID != "KB-database-overload" || hits[0].Kind != "semantic" {
		t.Fatalf("concept retrieval failed: %+v %v", hits, err)
	}
	all, err := s.Search(Query{IncludeSemantic: true, Limit: 100})
	if err != nil || len(all) < 5 {
		t.Fatalf("semantic corpus missing: %d %v", len(all), err)
	}
}

func TestContextAggregatesTwentyThousandLogsWithinBudget(t *testing.T) {
	var logText strings.Builder
	for i := 0; i < 20000; i++ {
		if i < 8213 {
			fmt.Fprintf(&logText, "14:31:42 ERROR PaymentRetryExceeded /checkout attempt=%d\n", i)
		} else if i < 8340 {
			fmt.Fprintf(&logText, "14:31:44 ERROR PaymentTimeout /checkout attempt=%d\n", i)
		} else if i < 8343 {
			fmt.Fprintf(&logText, "14:31:46 ERROR DatabaseError /checkout request=%d\n", i)
		} else {
			fmt.Fprintf(&logText, "14:31:48 INFO request completed /checkout request=%d\n", i)
		}
	}
	raw := []Record{
		{ID: "LOG-1", Source: "tools:search_logs/INC-1", Kind: "logs", Text: logText.String()},
		{ID: "METRIC-1", Source: "tools:get_metrics/INC-1", Kind: "metrics", Text: "error rate 1% -> 22%"},
	}
	ctx := BuildContext(raw, 2200)
	if ctx.SelectedChars > 2200 || ctx.SelectedChars != utf8.RuneCountInString(ctx.Text) || !utf8.ValidString(ctx.Text) {
		t.Fatalf("budget broken: %d", ctx.SelectedChars)
	}
	if ctx.RawChars < 900000 || ctx.EstimatedTokens <= 0 || ctx.RawChars/ctx.SelectedChars < 100 {
		t.Fatalf("logs not compressed: %+v", ctx)
	}
	if !strings.Contains(ctx.Text, "PaymentRetryExceeded count=8213") || !strings.Contains(ctx.Text, "PaymentTimeout count=127") || !strings.Contains(ctx.Text, "DatabaseError count=3") {
		t.Fatalf("counts or error signals lost: %s", ctx.Text)
	}
	if !strings.Contains(ctx.Text, "first=14:31:42") || !strings.Contains(ctx.Text, "endpoint=/checkout") || len(ctx.Sources) != 2 {
		t.Fatalf("context provenance/details missing: %+v", ctx)
	}
	if !strings.Contains(ctx.Text, "[LOG-1] log_summary") {
		t.Fatal("summary header does not preserve the parent evidence ID")
	}
	for _, group := range ctx.LogGroups {
		if len(group.RecordIDs) > 3 || len(group.RecordIDs) == 0 || group.Sources[0] != raw[0].Source {
			t.Fatalf("unbounded or missing provenance: %+v", group)
		}
	}
}

func TestUnicodeContextBudgetAndJSONLogReferences(t *testing.T) {
	for _, empty := range []string{"[]", `{"logs":[]}`, `{"entries":[]}`} {
		if groups := SummarizeLogs([]Record{{ID: "empty", Kind: "logs", Text: empty}}); len(groups) != 0 {
			t.Fatalf("empty logs became events: %+v", groups)
		}
	}
	jsonLogs := `[{"timestamp":"2026-10-01T14:32:00Z","level":"ERROR","message":"PaymentRetryExceeded","fields":{"endpoint":"/checkout"}},{"timestamp":"2026-10-01T14:31:42Z","level":"ERROR","message":"PaymentRetryExceeded","endpoint":"/checkout"}]`
	groups := SummarizeLogs([]Record{{ID: "LOG-2", Source: "raw.json", Kind: "logs", Text: jsonLogs}})
	if len(groups) != 1 || groups[0].Count != 2 || groups[0].FirstOccurrence != "2026-10-01T14:31:42Z" || groups[0].RecordIDs[1] != "LOG-2#2" {
		t.Fatalf("JSON aggregation wrong: %+v", groups)
	}
	for budget := 0; budget < 400; budget++ {
		ctx := BuildContext([]Record{{ID: "O1", Source: "日志源", Kind: "evidence", Text: strings.Repeat("支付错误🔥", 200)}}, budget)
		if !utf8.ValidString(ctx.Text) || utf8.RuneCountInString(ctx.Text) > budget {
			t.Fatalf("Unicode budget %d violated: %q", budget, ctx.Text)
		}
		if ctx.Text != "" && !strings.Contains(ctx.Text, "source: 日志源") {
			t.Fatal("partial source reference emitted")
		}
	}
}
