package events

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func NewStore(basePath string) (*Store, error) {
	dir := filepath.Join(basePath, ".flatrun")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "events.db")+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS incidents (
			correlation_key TEXT PRIMARY KEY,
			payload BLOB NOT NULL,
			last_event_at DATETIME NOT NULL
		);
		CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			incident_id TEXT NOT NULL,
			payload BLOB NOT NULL,
			occurred_at DATETIME NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_events_incident_id ON events(incident_id);
		CREATE INDEX IF NOT EXISTS idx_events_occurred_at ON events(occurred_at);
	`)
	return err
}

func (s *Store) Record(event Event, incident Incident) error {
	eventPayload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	incidentPayload, err := json.Marshal(incident)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO events (incident_id, payload, occurred_at) VALUES (?, ?, ?)`, incident.ID, eventPayload, event.OccurredAt); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO incidents (correlation_key, payload, last_event_at) VALUES (?, ?, ?)
		ON CONFLICT(correlation_key) DO UPDATE SET payload = excluded.payload, last_event_at = excluded.last_event_at`, incident.CorrelationKey, incidentPayload, incident.LastEventAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListIncidents() ([]Incident, error) {
	rows, err := s.db.Query(`SELECT payload FROM incidents ORDER BY last_event_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var incidents []Incident
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var incident Incident
		if err := json.Unmarshal(payload, &incident); err != nil {
			return nil, fmt.Errorf("decode incident: %w", err)
		}
		incidents = append(incidents, incident)
	}
	return incidents, rows.Err()
}

func (s *Store) Close() error {
	return s.db.Close()
}
