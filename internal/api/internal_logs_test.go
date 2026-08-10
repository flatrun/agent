package api

import (
	"encoding/json"
	"testing"
)

// The observability app reads this envelope to decide what is an incident. It is a wire
// contract between two packages that are compiled together but talk over HTTP, so the shape
// is pinned here and the app's watcher test decodes the same literal from the other side.
func TestInternalLogEnvelopeShape(t *testing.T) {
	raw := "web-1  | 2026-08-06T12:00:31.123456Z ERROR connection refused talking to redis"

	encoded, err := json.Marshal(logLine{Type: "log", Line: raw, Record: parseLogRecord(raw)})
	if err != nil {
		t.Fatal(err)
	}

	var decoded struct {
		Type   string `json:"type"`
		Line   string `json:"line"`
		Record struct {
			Timestamp string `json:"timestamp"`
			Service   string `json:"service"`
			Level     string `json:"level"`
			Message   string `json:"message"`
		} `json:"record"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("the envelope must decode into the shape the watcher expects: %v", err)
	}

	if decoded.Type != "log" {
		t.Errorf("type = %q, want log", decoded.Type)
	}
	if decoded.Line != raw {
		t.Errorf("line should be the untouched original, got %q", decoded.Line)
	}
	if decoded.Record.Service != "web-1" {
		t.Errorf("service should come from the compose prefix, got %q", decoded.Record.Service)
	}
	if decoded.Record.Level != "error" {
		t.Errorf("level should be parsed to a canonical name, got %q", decoded.Record.Level)
	}
	// The compose prefix and the leading timestamp are stripped; the level word stays in the
	// message, which is what the app fingerprints on.
	if decoded.Record.Message != "ERROR connection refused talking to redis" {
		t.Errorf("message should be the line without the compose prefix or timestamp, got %q", decoded.Record.Message)
	}
	if decoded.Record.Timestamp != "2026-08-06T12:00:31.123456Z" {
		t.Errorf("timestamp should be lifted out of the line, got %q", decoded.Record.Timestamp)
	}
}
