package api

import "testing"

// The sample lines below were captured from `docker compose logs --timestamps
// --no-color` (Compose v5.1.1) so the parser is tested against the real prefix
// and timestamp format, not an invented one.

func TestParseLogRecord_PlainTextWithTimestamp(t *testing.T) {
	raw := "emitter-1  | 2026-08-05T12:58:08.978479009Z plain hello"
	rec := parseLogRecord(raw)

	if rec.Service != "emitter-1" {
		t.Errorf("service = %q, want emitter-1", rec.Service)
	}
	if rec.Timestamp != "2026-08-05T12:58:08.978479009Z" {
		t.Errorf("timestamp = %q", rec.Timestamp)
	}
	if rec.Message != "plain hello" {
		t.Errorf("message = %q, want 'plain hello'", rec.Message)
	}
	if rec.Level != "" {
		t.Errorf("level = %q, want empty", rec.Level)
	}
	if rec.Raw != raw {
		t.Errorf("raw not preserved: %q", rec.Raw)
	}
}

func TestParseLogRecord_JSONLine(t *testing.T) {
	raw := `emitter-1  | 2026-08-05T12:58:08.978503560Z {"level":"warn","msg":"structured hi","user":"bob"}`
	rec := parseLogRecord(raw)

	if rec.Service != "emitter-1" {
		t.Errorf("service = %q", rec.Service)
	}
	if rec.Timestamp != "2026-08-05T12:58:08.978503560Z" {
		t.Errorf("timestamp = %q", rec.Timestamp)
	}
	if rec.Level != "warn" {
		t.Errorf("level = %q, want warn", rec.Level)
	}
	if rec.Message != "structured hi" {
		t.Errorf("message = %q, want 'structured hi'", rec.Message)
	}
	if rec.Fields["user"] != "bob" {
		t.Errorf("fields[user] = %q, want bob", rec.Fields["user"])
	}
}

func TestParseLogRecord_TextLevel(t *testing.T) {
	raw := "emitter-1  | 2026-08-05T12:58:08.978507756Z [ERROR] something failed"
	rec := parseLogRecord(raw)

	if rec.Level != "error" {
		t.Errorf("level = %q, want error", rec.Level)
	}
	if rec.Message != "[ERROR] something failed" {
		t.Errorf("message = %q", rec.Message)
	}
}

func TestParseLogRecord_NoPrefixOrTimestamp(t *testing.T) {
	raw := "a bare line with no compose prefix"
	rec := parseLogRecord(raw)

	if rec.Service != "" || rec.Timestamp != "" {
		t.Errorf("unexpected service=%q ts=%q", rec.Service, rec.Timestamp)
	}
	if rec.Message != raw {
		t.Errorf("message = %q, want the whole line", rec.Message)
	}
}

func TestCanonicalLevel(t *testing.T) {
	cases := map[string]string{
		"INFO": "info", "Information": "info", "notice": "info",
		"WARNING": "warn", "warn": "warn",
		"err": "error", "Error": "error",
		"CRITICAL": "fatal", "panic": "fatal",
		"debug": "debug", "trace": "trace",
	}
	for in, want := range cases {
		if got := canonicalLevel(in); got != want {
			t.Errorf("canonicalLevel(%q) = %q, want %q", in, got, want)
		}
	}
}
