package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

var planIDPattern = regexp.MustCompile(`^pln_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var ErrNotFound = fmt.Errorf("plan not found")

type Store struct {
	root string

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewStore(deploymentsPath string) *Store {
	return &Store{
		root:  filepath.Join(deploymentsPath, ".flatrun", "plans"),
		locks: map[string]*sync.Mutex{},
	}
}

func (s *Store) Root() string {
	return s.root
}

// LockResource serializes applies on the same resource. Returns the
// unlock function.
func (s *Store) LockResource(r Resource) func() {
	key := r.Type + "/" + r.ID
	s.mu.Lock()
	l, ok := s.locks[key]
	if !ok {
		l = &sync.Mutex{}
		s.locks[key] = l
	}
	s.mu.Unlock()
	l.Lock()
	return l.Unlock
}

func sanitizeSegment(seg string) string {
	var b strings.Builder
	for _, r := range seg {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		out = "_"
	}
	return out
}

func (s *Store) planPath(p *Plan) string {
	return filepath.Join(s.root, sanitizeSegment(p.Resource.Type), sanitizeSegment(p.Resource.ID), p.ID+".json")
}

// Save writes the plan atomically (tmp file + rename, fsynced) so a
// crash never leaves a torn plan file behind.
func (s *Store) Save(p *Plan) error {
	if !planIDPattern.MatchString(p.ID) {
		return fmt.Errorf("invalid plan id %q", p.ID)
	}
	path := s.planPath(p)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) findPath(id string) (string, error) {
	if !planIDPattern.MatchString(id) {
		return "", ErrNotFound
	}
	var found string
	err := filepath.WalkDir(s.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipAll
			}
			return err
		}
		if !d.IsDir() && d.Name() == id+".json" {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", ErrNotFound
	}
	return found, nil
}

func (s *Store) Get(id string) (*Plan, error) {
	path, err := s.findPath(id)
	if err != nil {
		return nil, err
	}
	return readPlanFile(path)
}

func readPlanFile(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("corrupt plan file %s: %w", filepath.Base(path), err)
	}
	return &p, nil
}

type ListFilter struct {
	ResourceType string
	ResourceID   string
	Status       string
}

func (s *Store) List(filter ListFilter) ([]*Plan, error) {
	var plans []*Plan
	err := filepath.WalkDir(s.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipAll
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		p, readErr := readPlanFile(path)
		if readErr != nil {
			return nil
		}
		if filter.ResourceType != "" && p.Resource.Type != filter.ResourceType {
			return nil
		}
		if filter.ResourceID != "" && p.Resource.ID != filter.ResourceID {
			return nil
		}
		if filter.Status != "" && p.Status != filter.Status {
			return nil
		}
		plans = append(plans, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].CreatedAt.After(plans[j].CreatedAt) })
	return plans, nil
}

func (s *Store) Delete(id string) error {
	path, err := s.findPath(id)
	if err != nil {
		return err
	}
	return os.Remove(path)
}
