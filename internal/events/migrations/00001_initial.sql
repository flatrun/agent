-- +goose Up
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

-- +goose Down
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS incidents;
