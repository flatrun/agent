package events

import (
	"testing"
	"time"
)

func TestStoreRecordsMigrationVersionAndPreservesIncidents(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	var version int64
	if err := store.db.QueryRow(`SELECT MAX(version_id) FROM events_schema_version WHERE is_applied = 1`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != 1 {
		t.Fatalf("schema version = %d, want 1", version)
	}

	now := time.Now().UTC()
	event := Event{ID: "event-1", Source: "capacity", Type: "host.pressure", Severity: SeverityWarning, OccurredAt: now}
	incident := Incident{ID: "incident-1", CorrelationKey: "node:prod-1", Status: IncidentOpen, Severity: SeverityWarning, LastEventAt: now}
	if err := store.Record(event, incident); err != nil {
		t.Fatalf("Record failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	incidents, err := reopened.ListIncidents()
	if err != nil {
		t.Fatalf("ListIncidents failed: %v", err)
	}
	if len(incidents) != 1 || incidents[0].ID != "incident-1" {
		t.Fatalf("incidents = %#v", incidents)
	}
}
