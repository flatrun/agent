package config

import (
	"testing"
	"time"
)

func TestWalkFlattensKnownKeys(t *testing.T) {
	cfg := &Config{}
	setDefaults(cfg)
	cfg.API.Port = 9090
	cfg.Cleanup.Timeout = 90 * time.Second

	entries := Walk(cfg)
	if len(entries) == 0 {
		t.Fatal("Walk returned no entries")
	}

	keys := make(map[string]Entry, len(entries))
	for _, e := range entries {
		keys[e.Key] = e
	}

	if _, ok := keys["api.port"]; !ok {
		t.Fatalf("expected api.port in entries, got: %v", entryKeys(entries))
	}
	if v, _ := keys["api.port"].Value.(int); v != 9090 {
		t.Errorf("api.port = %v, want 9090", keys["api.port"].Value)
	}

	if v, _ := keys["cleanup.timeout"].Value.(string); v != "1m30s" {
		t.Errorf("cleanup.timeout = %v, want 1m30s", keys["cleanup.timeout"].Value)
	}

	if v, _ := keys["default_timeout"].Default.(string); v != "2m0s" {
		t.Errorf("default_timeout default = %v, want 2m0s", keys["default_timeout"].Default)
	}
}

func TestWalkHidesSensitiveValues(t *testing.T) {
	cfg := &Config{}
	setDefaults(cfg)
	cfg.Auth.JWTSecret = "actual-secret"

	entries := Walk(cfg)
	for _, e := range entries {
		if e.Key == "auth.jwt_secret" {
			if e.Value != nil {
				t.Errorf("expected nil value for sensitive key, got %v", e.Value)
			}
			if !e.Sensitive {
				t.Error("expected Sensitive=true for auth.jwt_secret")
			}
			return
		}
	}
	t.Fatalf("auth.jwt_secret not found in entries: %v", entryKeys(entries))
}

func TestSetCoercesTypes(t *testing.T) {
	cfg := &Config{}
	setDefaults(cfg)

	if err := Set(cfg, "api.port", "1234"); err != nil {
		t.Fatalf("Set api.port from string: %v", err)
	}
	if cfg.API.Port != 1234 {
		t.Errorf("api.port = %d, want 1234", cfg.API.Port)
	}

	if err := Set(cfg, "cleanup.timeout", "45s"); err != nil {
		t.Fatalf("Set cleanup.timeout from string: %v", err)
	}
	if cfg.Cleanup.Timeout != 45*time.Second {
		t.Errorf("cleanup.timeout = %v, want 45s", cfg.Cleanup.Timeout)
	}

	if err := Set(cfg, "api.enable_cors", true); err != nil {
		t.Fatalf("Set api.enable_cors from bool: %v", err)
	}
	if !cfg.API.EnableCORS {
		t.Errorf("api.enable_cors = false, want true")
	}
}

func TestSetRejectsUnknownKey(t *testing.T) {
	cfg := &Config{}
	setDefaults(cfg)
	if err := Set(cfg, "no.such.thing", "x"); err == nil {
		t.Fatal("expected error for unknown key, got nil")
	}
}

func TestSetRejectsHiddenKey(t *testing.T) {
	cfg := &Config{}
	setDefaults(cfg)
	if err := Set(cfg, "auth.jwt_secret", "x"); err == nil {
		t.Fatal("expected hidden-key error, got nil")
	}
	if cfg.Auth.JWTSecret == "x" {
		t.Error("hidden key was written despite rejection")
	}
}

func TestGetReturnsEntry(t *testing.T) {
	cfg := &Config{}
	setDefaults(cfg)
	cfg.Cleanup.Timeout = 30 * time.Second

	e, err := Get(cfg, "cleanup.timeout")
	if err != nil {
		t.Fatalf("Get cleanup.timeout: %v", err)
	}
	if e.Type != "duration" {
		t.Errorf("type = %q, want duration", e.Type)
	}
	if e.Value.(string) != "30s" {
		t.Errorf("value = %v, want 30s", e.Value)
	}
}

func entryKeys(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Key)
	}
	return out
}
