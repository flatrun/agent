package auth

import (
	"testing"
	"time"
)

func TestActorForTokenStringUpdatesAPIKeyLastUsed(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	manager.config.Enabled = true

	user, _ := manager.CreateUser("wskeyuser", "", "pass", RoleOperator, nil)
	key, plainKey, err := manager.CreateAPIKey(user.ID, "WS Key", "", "", nil, nil, time.Time{})
	if err != nil {
		t.Fatalf("CreateAPIKey failed: %v", err)
	}

	mw := NewMiddlewareWithManager(manager.config, manager)
	actor, err := mw.ActorForTokenString(plainKey, "203.0.113.10")
	if err != nil {
		t.Fatalf("ActorForTokenString failed: %v", err)
	}
	if actor.APIKey == nil || actor.APIKey.ID != key.ID {
		t.Fatalf("expected API key actor, got %#v", actor)
	}

	updated, err := manager.GetAPIKey(key.ID)
	if err != nil {
		t.Fatalf("GetAPIKey failed: %v", err)
	}
	if updated.LastUsedAt.IsZero() {
		t.Fatal("expected LastUsedAt to be set")
	}
	if updated.LastUsedIP != "203.0.113.10" {
		t.Fatalf("expected LastUsedIP to be 203.0.113.10, got %q", updated.LastUsedIP)
	}
}

func TestActorForTokenStringLegacyKeyReturnsAdminActor(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	manager.config.Enabled = true

	mw := NewMiddlewareWithManager(manager.config, manager)
	actor, err := mw.ActorForTokenString("legacy-key-1", "203.0.113.10")
	if err != nil {
		t.Fatalf("ActorForTokenString failed: %v", err)
	}
	if actor.Role != RoleAdmin || actor.Type != "legacy_key" {
		t.Fatalf("expected legacy admin actor, got %#v", actor)
	}
}
