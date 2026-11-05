package runs

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/flatrun/agent/internal/runtimes"
	_ "modernc.org/sqlite"
)

type Status string

const (
	StatusQueued    Status = "queued"
	StatusStarting  Status = "starting"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusTimedOut  Status = "timed_out"
	StatusCancelled Status = "cancelled"
)

func (s Status) Finished() bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusTimedOut || s == StatusCancelled
}

type Spec struct {
	Runtime        runtimes.Type     `json:"runtime"`
	Image          string            `json:"image"`
	Command        []string          `json:"command,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	WorkDir        string            `json:"workdir,omitempty"`
	CPUs           float64           `json:"cpus,omitempty"`
	MemoryBytes    uint64            `json:"memory_bytes,omitempty"`
	Egress         bool              `json:"egress"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	ArtifactPaths  []string          `json:"artifact_paths,omitempty"`
}

func (s *Spec) Validate() error {
	if s.Runtime == "" {
		s.Runtime = runtimes.TypeDocker
	}
	if s.Image == "" {
		return errors.New("image is required")
	}
	if s.TimeoutSeconds == 0 {
		s.TimeoutSeconds = 600
	}
	if s.TimeoutSeconds < 0 || s.TimeoutSeconds > 3600 {
		return errors.New("timeout must be positive and at most one hour")
	}
	if s.CPUs < 0 || math.IsNaN(s.CPUs) || math.IsInf(s.CPUs, 0) || s.CPUs > float64(math.MaxInt64)/1e9 || s.MemoryBytes > math.MaxInt64 {
		return errors.New("invalid resource limits")
	}
	if len(s.ArtifactPaths) > 16 {
		return errors.New("at most 16 artifact paths are allowed")
	}
	names := map[string]bool{}
	for _, artifactPath := range s.ArtifactPaths {
		baseName := path.Base(artifactPath)
		if !path.IsAbs(artifactPath) || baseName == "/" || baseName == "." || baseName == ".." || strings.ContainsRune(artifactPath, 0) || names[baseName] {
			return errors.New("artifact paths must be absolute with unique base names")
		}
		names[baseName] = true
	}
	return nil
}

type Artifact struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type Run struct {
	ID            string        `json:"id"`
	OwnerUserID   int64         `json:"owner_user_id"`
	OwnerUsername string        `json:"owner_username"`
	Runtime       runtimes.Type `json:"runtime"`
	Spec          Spec          `json:"spec"`
	Status        Status        `json:"status"`
	ExitCode      *int          `json:"exit_code"`
	Error         string        `json:"error,omitempty"`
	Artifacts     []Artifact    `json:"artifacts"`
	CreatedAt     time.Time     `json:"created_at"`
	StartedAt     *time.Time    `json:"started_at,omitempty"`
	FinishedAt    *time.Time    `json:"finished_at,omitempty"`
}

type Store struct{ db *sql.DB }

func NewStore(root string) (*Store, error) {
	dir := filepath.Join(root, ".flatrun")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "runs.db")+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS runs (id TEXT PRIMARY KEY, owner INTEGER NOT NULL, status TEXT NOT NULL, record TEXT NOT NULL); CREATE INDEX IF NOT EXISTS idx_runs_owner ON runs(owner)`); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Save(run *Run) error {
	record, err := json.Marshal(run)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO runs VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,record=excluded.record`, run.ID, run.OwnerUserID, run.Status, string(record))
	return err
}

func (s *Store) Get(id string) (*Run, error) {
	var record string
	if err := s.db.QueryRow(`SELECT record FROM runs WHERE id=?`, id).Scan(&record); err != nil {
		return nil, err
	}
	var run Run
	if err := json.Unmarshal([]byte(record), &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Store) List(owner *int64, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	query := `SELECT record FROM runs`
	args := []any{}
	if owner != nil {
		query += ` WHERE owner=?`
		args = append(args, *owner)
	}
	query += ` ORDER BY rowid DESC LIMIT ?`
	args = append(args, limit)
	return s.queryRuns(query, args...)
}

func (s *Store) queryRuns(query string, args ...any) ([]Run, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Run{}
	for rows.Next() {
		var record string
		var run Run
		if err := rows.Scan(&record); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(record), &run); err != nil {
			return nil, err
		}
		result = append(result, run)
	}
	return result, rows.Err()
}

func (s *Store) Recover() error {
	pendingRuns, err := s.queryRuns(`SELECT record FROM runs WHERE status IN ('queued', 'starting', 'running')`)
	if err != nil {
		return err
	}
	for _, run := range pendingRuns {
		run.Status = StatusFailed
		run.Error = "agent restarted"
		now := time.Now().UTC()
		run.FinishedAt = &now
		if err := s.Save(&run); err != nil {
			return err
		}
	}
	return nil
}
