package auth

import (
	"strings"
	"testing"
)

func TestGenerateAPIKey(t *testing.T) {
	plainKey, keyHash, keyID, prefix, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey failed: %v", err)
	}

	if plainKey == "" {
		t.Error("GenerateAPIKey returned empty plainKey")
	}

	if !strings.HasPrefix(plainKey, "fr_") {
		t.Error("API key should start with 'fr_' prefix")
	}

	if keyHash == "" {
		t.Error("GenerateAPIKey returned empty keyHash")
	}

	if keyID == "" {
		t.Error("GenerateAPIKey returned empty keyID")
	}

	if prefix == "" {
		t.Error("GenerateAPIKey returned empty prefix")
	}

	if !strings.HasSuffix(prefix, "...") {
		t.Error("Prefix should end with '...'")
	}
}

func TestGenerateAPIKeyUniqueness(t *testing.T) {
	key1, hash1, id1, _, _ := GenerateAPIKey()
	key2, hash2, id2, _, _ := GenerateAPIKey()

	if key1 == key2 {
		t.Error("GenerateAPIKey should generate unique keys")
	}

	if hash1 == hash2 {
		t.Error("GenerateAPIKey should generate unique hashes")
	}

	if id1 == id2 {
		t.Error("GenerateAPIKey should generate unique IDs")
	}
}

func TestHashAPIKey(t *testing.T) {
	key := "fr_test_api_key_12345"

	hash1 := HashAPIKey(key)
	hash2 := HashAPIKey(key)

	if hash1 != hash2 {
		t.Error("HashAPIKey should return same hash for same key")
	}

	if hash1 == key {
		t.Error("HashAPIKey should not return plaintext key")
	}

	if len(hash1) != 64 {
		t.Errorf("SHA-256 hash should be 64 hex characters, got %d", len(hash1))
	}
}

func TestGenerateUID(t *testing.T) {
	uid1, err := GenerateUID()
	if err != nil {
		t.Fatalf("GenerateUID failed: %v", err)
	}

	uid2, _ := GenerateUID()

	if uid1 == "" {
		t.Error("GenerateUID returned empty string")
	}

	if uid1 == uid2 {
		t.Error("GenerateUID should generate unique values")
	}

	if len(uid1) != 32 {
		t.Errorf("UID should be 32 hex characters, got %d", len(uid1))
	}
}

func TestGenerateSessionID(t *testing.T) {
	sid1, err := GenerateSessionID()
	if err != nil {
		t.Fatalf("GenerateSessionID failed: %v", err)
	}

	sid2, _ := GenerateSessionID()

	if sid1 == "" {
		t.Error("GenerateSessionID returned empty string")
	}

	if sid1 == sid2 {
		t.Error("GenerateSessionID should generate unique values")
	}
}
