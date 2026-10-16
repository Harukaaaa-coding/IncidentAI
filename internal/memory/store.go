// Package memory persists incident state and reviewable incident experience.
package memory

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Memory is an experience, not an established fact until a human confirms it.
type Memory struct {
	ID           string   `json:"id"`
	IncidentID   string   `json:"incident_id"`
	Service      string   `json:"service"`
	IncidentType string   `json:"incident_type"`
	Severity     string   `json:"severity"`
	Date         string   `json:"date"`
	RootCause    string   `json:"root_cause"`
	Resolution   string   `json:"resolution"`
	Status       string   `json:"status"`
	Environment  string   `json:"environment"`
	Pattern      string   `json:"pattern"`
	Signal       string   `json:"signal"`
	Diagnostic   string   `json:"diagnostic"`
	Source       string   `json:"source"`
	Evidence     []string `json:"evidence"`
	ReviewedBy   string   `json:"reviewed_by,omitempty"`
	ReviewedAt   string   `json:"reviewed_at,omitempty"`
}

type auditEvent struct {
	At             string `json:"at"`
	Action         string `json:"action"`
	PreviousStatus string `json:"previous_status,omitempty"`
	Memory         Memory `json:"memory"`
}

// Store supports concurrent goroutines and Store instances in one process.
// Use one application process for a data directory (no cross-process lock).
type Store struct {
	mu           *sync.Mutex
	dataDir      string
	knowledgeDir string
}

var directoryLocks sync.Map
var safeID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

func NewStore(dataDir, knowledgeDir string) (*Store, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("memory data directory is required")
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	lock, _ := directoryLocks.LoadOrStore(strings.ToLower(filepath.Clean(abs)), &sync.Mutex{})
	s := &Store{mu: lock.(*sync.Mutex), dataDir: abs, knowledgeDir: knowledgeDir}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(abs, "incidents"), 0700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(abs, "memories"), 0700); err != nil {
		return nil, err
	}
	if _, err := s.readMemories(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) SaveIncident(id string, value any) error {
	if !safeID.MatchString(id) {
		return errors.New("invalid incident ID")
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return atomicWrite(filepath.Join(s.dataDir, "incidents", id+".json"), b)
}

func (s *Store) LoadIncident(id string) (json.RawMessage, error) {
	if !safeID.MatchString(id) {
		return nil, errors.New("invalid incident ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(filepath.Join(s.dataDir, "incidents", id+".json"))
	if err == nil && !json.Valid(b) {
		return nil, fmt.Errorf("corrupt incident %s", id)
	}
	return json.RawMessage(b), err
}

func (s *Store) ListIncidents() ([]json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := filepath.Glob(filepath.Join(s.dataDir, "incidents", "*.json"))
	if err != nil {
		return nil, err
	}
	items := make([]json.RawMessage, 0, len(files))
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if !json.Valid(b) {
			return nil, fmt.Errorf("corrupt incident file %s", filepath.Base(path))
		}
		items = append(items, json.RawMessage(b))
	}
	return items, nil
}

func (s *Store) SaveCandidate(m Memory) (Memory, error) {
	if strings.TrimSpace(m.IncidentID) == "" || strings.TrimSpace(m.RootCause) == "" {
		return Memory{}, errors.New("candidate requires incident_id and root_cause")
	}
	if m.ID == "" {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return Memory{}, err
		}
		m.ID = "MEM-" + hex.EncodeToString(random[:])
	}
	if !safeID.MatchString(m.ID) {
		return Memory{}, errors.New("invalid memory ID")
	}
	m.Status, m.ReviewedAt, m.ReviewedBy = "candidate", "", ""
	if m.Date == "" {
		m.Date = time.Now().UTC().Format(time.RFC3339)
	}
	if m.Source == "" {
		m.Source = "incident:" + m.IncidentID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	memories, err := s.readMemories()
	if err != nil {
		return Memory{}, err
	}
	if _, exists := memories[m.ID]; exists {
		return Memory{}, fmt.Errorf("memory %s already exists", m.ID)
	}
	if err := s.appendEvent(auditEvent{At: time.Now().UTC().Format(time.RFC3339Nano), Action: "candidate_created", Memory: m}); err != nil {
		return Memory{}, err
	}
	return m, nil
}

func (s *Store) Review(id, status, reviewer string) (Memory, error) {
	switch status {
	case "confirmed", "probable", "rejected", "outdated":
	default:
		return Memory{}, errors.New("review status must be confirmed, probable, rejected, or outdated")
	}
	if strings.TrimSpace(reviewer) == "" {
		return Memory{}, errors.New("a human reviewer identity is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	memories, err := s.readMemories()
	if err != nil {
		return Memory{}, err
	}
	m, ok := memories[id]
	if !ok {
		return Memory{}, fmt.Errorf("memory %s: %w", id, os.ErrNotExist)
	}
	hasEvidence := false
	for _, reference := range m.Evidence {
		if strings.TrimSpace(reference) != "" {
			hasEvidence = true
		}
	}
	if status == "confirmed" && (!hasEvidence || strings.TrimSpace(m.Resolution) == "") {
		return Memory{}, errors.New("confirmed memory requires evidence references and resolution")
	}
	previous := m.Status
	m.Status, m.ReviewedBy, m.ReviewedAt = status, reviewer, time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.appendEvent(auditEvent{At: m.ReviewedAt, Action: "human_review", PreviousStatus: previous, Memory: m}); err != nil {
		return Memory{}, err
	}
	return m, nil
}

func (s *Store) ListMemories() ([]Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	memories, err := s.readMemories()
	if err != nil {
		return nil, err
	}
	result := make([]Memory, 0, len(memories))
	for _, m := range memories {
		result = append(result, m)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Date != result[j].Date {
			return result[i].Date > result[j].Date
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

// The append-only audit is also the authoritative episodic store. A failed
// write therefore cannot update memory without an associated review event.
func (s *Store) appendEvent(event auditEvent) error {
	b, err := json.Marshal(event)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.dataDir, "memories", "audit.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (s *Store) readMemories() (map[string]Memory, error) {
	result := make(map[string]Memory)
	f, err := os.Open(filepath.Join(s.dataDir, "memories", "audit.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	for line := 1; ; line++ {
		b, readErr := reader.ReadBytes('\n')
		if len(b) > 0 {
			var event auditEvent
			if b[len(b)-1] != '\n' || json.Unmarshal(b, &event) != nil || event.Memory.ID == "" {
				return nil, fmt.Errorf("memory audit corrupt or interrupted at line %d; restore the log before continuing", line)
			}
			result[event.Memory.ID] = event.Memory
		}
		if errors.Is(readErr, io.EOF) {
			return result, nil
		}
		if readErr != nil {
			return nil, readErr
		}
	}
}

func atomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".incident-*.tmp")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temp, path)
}
