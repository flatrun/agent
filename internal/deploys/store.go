// Package deploys keeps the per-deployment history of deploys: which revision
// was deployed, what triggered it, how it went and its output. Rollback picks a
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

const (
	maxRecords = 50
	// MaxLogBytes caps one deploy's stored output.
	MaxLogBytes = 1 << 20
)

type Status string

const (
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

// Phase is where a deploy is in its life. Queued, building and deploying are
// set while it runs. Active marks the newest successful deploy, which is the
// one serving; older successful ones read as removed.
type Phase string

const (
	PhaseQueued    Phase = "queued"
	PhaseBuilding  Phase = "building"
	PhaseDeploying Phase = "deploying"
	PhaseActive    Phase = "active"
	PhaseFailed    Phase = "failed"
	PhaseRemoved   Phase = "removed"
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
	Action     string     `json:"action,omitempty"`
	Revision   string     `json:"revision,omitempty"`
	Branch     string     `json:"branch,omitempty"`
	Message    string     `json:"message,omitempty"`
	Trigger    Trigger    `json:"trigger"`
	Status     Status     `json:"status"`
	Phase      Phase      `json:"phase"`
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
	rec.Phase = PhaseQueued
	rec.StartedAt = time.Now().UTC()
	if rec.Trigger == "" {
		rec.Trigger = TriggerManual
	}
	records = append([]Record{rec}, records...)
	if len(records) > maxRecords {
		for _, dropped := range records[maxRecords:] {
			_ = os.Remove(s.logPath(deployment, dropped.ID))
		}
		records = records[:maxRecords]
	}
	return rec, s.save(deployment, records)
}

// SetPhase moves a running record to another phase.
func (s *Store) SetPhase(deployment, id string, phase Phase) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	records, err := s.load(deployment)
	if err != nil {
		return err
	}
	for i := range records {
		if records[i].ID == id {
			records[i].Phase = phase
			return s.save(deployment, records)
		}
	}
	return ErrNotFound
}

// Finish closes a running record. A non-empty revision replaces the requested
// one with the commit that was actually fetched, and message is that commit's
// subject.
func (s *Store) Finish(deployment, id, revision, message string, deployErr error) error {
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
		if message != "" {
			records[i].Message = message
		}
		if deployErr != nil {
			records[i].Status = StatusFailed
			records[i].Phase = PhaseFailed
			records[i].Error = deployErr.Error()
		} else {
			records[i].Status = StatusSucceeded
			records[i].Phase = PhaseActive
		}
		return s.save(deployment, records)
	}
	return ErrNotFound
}

// List returns the records newest first. Only the newest successful deploy
// is active; earlier successful ones are reported as removed.
func (s *Store) List(deployment string) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.load(deployment)
	if err != nil {
		return nil, err
	}
	seenActive := false
	for i := range records {
		switch records[i].Status {
		case StatusSucceeded:
			if seenActive {
				records[i].Phase = PhaseRemoved
			} else {
				records[i].Phase = PhaseActive
				seenActive = true
			}
		case StatusFailed:
			records[i].Phase = PhaseFailed
		case StatusRunning:
			if records[i].Phase == "" {
				records[i].Phase = PhaseQueued
			}
		}
	}
	return records, nil
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
	if err := os.RemoveAll(s.logDir(deployment)); err != nil {
		return err
	}
	err := os.Remove(s.path(deployment))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Store) logDir(deployment string) string {
	return filepath.Join(s.root, filepath.Base(deployment))
}

func (s *Store) logPath(deployment, id string) string {
	return filepath.Join(s.logDir(deployment), filepath.Base(id)+".log")
}

// ReadLog returns a deploy's stored output.
func (s *Store) ReadLog(deployment, id string) (string, error) {
	data, err := os.ReadFile(s.logPath(deployment, id))
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotFound
	}
	return string(data), err
}

// LogWriter appends a deploy's output lines to its log file and stops at
// MaxLogBytes.
type LogWriter struct {
	mu        sync.Mutex
	file      *os.File
	written   int
	truncated bool
}

func (s *Store) OpenLog(deployment, id string) (*LogWriter, error) {
	if err := os.MkdirAll(s.logDir(deployment), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.logPath(deployment, id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	return &LogWriter{file: f}, nil
}

func (w *LogWriter) Line(line string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.truncated {
		return
	}
	if w.written+len(line)+1 > MaxLogBytes {
		w.truncated = true
		_, _ = w.file.WriteString("[output truncated]\n")
		return
	}
	n, _ := w.file.WriteString(line + "\n")
	w.written += n
}

func (w *LogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
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
