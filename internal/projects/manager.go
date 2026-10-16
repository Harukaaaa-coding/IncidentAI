package projects

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var idPattern = regexp.MustCompile(`^(project|run)-[a-f0-9]{24}$`)

type Manager struct {
	dir string
	mu  sync.Mutex
}

func NewManager(dataDir string) (*Manager, error) {
	dir, err := filepath.Abs(filepath.Join(dataDir, "local-projects"))
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return &Manager{dir: dir}, nil
}

func newID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + "-" + hex.EncodeToString(b)
}
func validID(id, prefix string) bool {
	return idPattern.MatchString(id) && strings.HasPrefix(id, prefix+"-")
}

func (m *Manager) List() ([]Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.list()
}
func (m *Manager) list() ([]Project, error) {
	items := []Project{}
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || !validID(strings.TrimSuffix(entry.Name(), ".json"), "project") {
			continue
		}
		var p Project
		if err = readJSON(filepath.Join(m.dir, entry.Name()), &p); err != nil {
			return nil, fmt.Errorf("read project metadata: %w", err)
		}
		items = append(items, p)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	return items, nil
}
func (m *Manager) Create(in Input) (Project, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 120 {
		return Project{}, errors.New("project name must contain 1–120 bytes")
	}
	repo, err := safeRepository(in.RepositoryPath)
	if err != nil {
		return Project{}, err
	}
	logs, err := safeLogPath(repo, in.LogPath)
	if err != nil {
		return Project{}, err
	}
	health := strings.TrimSpace(in.HealthURL)
	if health != "" {
		if _, err = healthTarget(health); err != nil {
			return Project{}, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	items, err := m.list()
	if err != nil {
		return Project{}, err
	}
	for _, p := range items {
		if strings.EqualFold(p.RepositoryPath, repo) {
			return Project{}, errors.New("this repository is already registered")
		}
	}
	p := Project{ID: newID("project"), Name: Redact(name), RepositoryPath: repo, LogPath: logs, HealthURL: health, CreatedAt: time.Now().UTC()}
	if err = atomicJSON(filepath.Join(m.dir, p.ID+".json"), p); err != nil {
		return Project{}, err
	}
	return p, nil
}
func (m *Manager) Get(id string) (Project, error) { m.mu.Lock(); defer m.mu.Unlock(); return m.get(id) }
func (m *Manager) get(id string) (Project, error) {
	if !validID(id, "project") {
		return Project{}, errors.New("invalid project ID")
	}
	var p Project
	err := readJSON(filepath.Join(m.dir, id+".json"), &p)
	return p, err
}
func (m *Manager) ListInvestigations(projectID string) ([]Investigation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.get(projectID); err != nil {
		return nil, err
	}
	result := []Investigation{}
	entries, err := os.ReadDir(filepath.Join(m.dir, projectID))
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || !validID(strings.TrimSuffix(entry.Name(), ".json"), "run") {
			continue
		}
		var run Investigation
		if err = readJSON(filepath.Join(m.dir, projectID, entry.Name()), &run); err != nil {
			return nil, err
		}
		result = append(result, run)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].StartedAt.After(result[j].StartedAt) })
	return result, nil
}
func (m *Manager) GetInvestigation(projectID, runID string) (Investigation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !validID(runID, "run") {
		return Investigation{}, errors.New("invalid investigation ID")
	}
	if _, err := m.get(projectID); err != nil {
		return Investigation{}, err
	}
	var run Investigation
	err := readJSON(filepath.Join(m.dir, projectID, runID+".json"), &run)
	return run, err
}
func (m *Manager) saveRun(run Investigation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	dir := filepath.Join(m.dir, run.ProjectID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return atomicJSON(filepath.Join(dir, run.ID+".json"), run)
}
func readJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(f).Decode(v)
}
func atomicJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pending-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	cerr := f.Close()
	if err != nil {
		return err
	}
	if cerr != nil {
		return cerr
	}
	return os.Rename(tmp, path)
}
