// Package deploys keeps the per-deployment history of source deploys: which
// revision was deployed, what triggered it and how it ended. Rollback picks a
// past record and redeploys its revision.
package deploys

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxRecords = 50

type Status string

const (
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

type Trigger string

const (
	TriggerManual   Trigger = "manual"
	TriggerPush     Trigger = "push"
	TriggerPR       Trigger = "pull_request"
	TriggerRollback Trigger = "rollback"
)

type Record struct {
	ID         string     `json:"id"`
	Revision   string     `json:"revision,omitempty"`
	Branch     string     `json:"branch,omitempty"`
	Trigger    Trigger    `json:"trigger"`
	Status     Status     `json:"status"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

var ErrNotFound = errors.New("deploy record not found")

type Store struct {
	root string
	mu   sync.Mutex
}

func NewStore(deploymentsPath string) *Store {
	return &Store{root: filepath.Join(deploymentsPath, ".flatrun", "deploys")}
}

// Start records a new running deploy, newest first, and returns it.
func (s *Store) Start(deployment string, rec Record) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	records, err := s.load(deployment)
	if err != nil {
		return Record{}, err
	}
	rec.ID = newID()
	rec.Status = StatusRunning
	rec.StartedAt = time.Now().UTC()
	if rec.Trigger == "" {
		rec.Trigger = TriggerManual
	}
	records = append([]Record{rec}, records...)
	if len(records) > maxRecords {
		records = records[:maxRecords]
	}
	return rec, s.save(deployment, records)
}

// Finish closes a running record. A non-empty revision replaces the requested
// one with the commit that was actually fetched.
func (s *Store) Finish(deployment, id, revision string, deployErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	records, err := s.load(deployment)
	if err != nil {
		return err
	}
	for i := range records {
		if records[i].ID != id {
			continue
		}
		now := time.Now().UTC()
		records[i].FinishedAt = &now
		if revision != "" {
			records[i].Revision = revision
		}
		if deployErr != nil {
			records[i].Status = StatusFailed
			records[i].Error = deployErr.Error()
		} else {
			records[i].Status = StatusSucceeded
		}
		return s.save(deployment, records)
	}
	return ErrNotFound
}

func (s *Store) List(deployment string) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(deployment)
}

func (s *Store) Get(deployment, id string) (Record, error) {
	records, err := s.List(deployment)
	if err != nil {
		return Record{}, err
	}
	for _, r := range records {
		if r.ID == id {
			return r, nil
		}
	}
	return Record{}, ErrNotFound
}

func (s *Store) Delete(deployment string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path(deployment))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Store) path(deployment string) string {
	return filepath.Join(s.root, filepath.Base(deployment)+".json")
}

func (s *Store) load(deployment string) ([]Record, error) {
	data, err := os.ReadFile(s.path(deployment))
	if errors.Is(err, os.ErrNotExist) {
		return []Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	var records []Record
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("corrupt deploy history for %s: %w", deployment, err)
	}
	return records, nil
}

func (s *Store) save(deployment string, records []Record) error {
	if err := os.MkdirAll(s.root, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(deployment) + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(deployment))
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
