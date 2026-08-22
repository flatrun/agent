package autoscale

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Store struct {
	conn *sql.DB
	mu   sync.RWMutex
}

func NewStore(deploymentsPath string) (*Store, error) {
	dir := filepath.Join(deploymentsPath, ".flatrun")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "autoscale.db")+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	store := &Store{conn: conn}
	if err := store.migrate(); err != nil {
		conn.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) migrate() error {
	migrations, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, s.conn, migrations, goose.WithTableName("autoscale_schema_version"))
	if err != nil {
		return err
	}
	_, err = provider.Up(context.Background())
	return err
}

func (s *Store) Close() error {
	return s.conn.Close()
}

func (s *Store) Policy(deployment string) (Policy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var raw string
	err := s.conn.QueryRow(`SELECT policy_json FROM autoscale_policies WHERE deployment = ?`, deployment).Scan(&raw)
	if err == sql.ErrNoRows {
		return DefaultPolicy(), nil
	}
	if err != nil {
		return Policy{}, err
	}
	var policy Policy
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

func (s *Store) SetPolicy(deployment string, policy Policy) error {
	if err := ValidatePolicy(policy); err != nil {
		return err
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.conn.Exec(`
		INSERT INTO autoscale_policies (deployment, policy_json, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(deployment) DO UPDATE SET policy_json = excluded.policy_json, updated_at = CURRENT_TIMESTAMP`, deployment, raw)
	return err
}

func (s *Store) State(deployment string) (State, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var raw string
	err := s.conn.QueryRow(`SELECT state_json FROM autoscale_states WHERE deployment = ?`, deployment).Scan(&raw)
	if err == sql.ErrNoRows {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (s *Store) SetState(deployment string, state State) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.conn.Exec(`
		INSERT INTO autoscale_states (deployment, state_json, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(deployment) DO UPDATE SET state_json = excluded.state_json, updated_at = CURRENT_TIMESTAMP`, deployment, raw)
	return err
}

func (s *Store) ActiveStates() (map[string]State, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.conn.Query(`SELECT deployment, state_json FROM autoscale_states`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make(map[string]State)
	for rows.Next() {
		var deployment string
		var raw string
		if err := rows.Scan(&deployment, &raw); err != nil {
			return nil, err
		}
		var state State
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			return nil, err
		}
		if state.Active {
			states[deployment] = state
		}
	}
	return states, rows.Err()
}
