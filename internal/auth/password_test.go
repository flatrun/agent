package auth

import (
	"testing"
)

func TestHashPassword(t *testing.T) {
	password := "testpassword123"

	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	if hash == "" {
		t.Error("HashPassword returned empty hash")
	}

	if hash == password {
		t.Error("HashPassword returned plaintext password")
	}
}

func TestHashPasswordDifferentHashes(t *testing.T) {
	password := "testpassword123"

	hash1, _ := HashPassword(password)
	hash2, _ := HashPassword(password)

	if hash1 == hash2 {
		t.Error("HashPassword should generate different hashes for same password (due to salt)")
	}
}

func TestVerifyPassword(t *testing.T) {
	password := "testpassword123"

	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	if !VerifyPassword(password, hash) {
		t.Error("VerifyPassword should return true for correct password")
	}
}

func TestVerifyPasswordWrong(t *testing.T) {
	password := "testpassword123"
	wrongPassword := "wrongpassword"

	hash, _ := HashPassword(password)

	if VerifyPassword(wrongPassword, hash) {
		t.Error("VerifyPassword should return false for wrong password")
	}
}

func TestVerifyPasswordEmpty(t *testing.T) {
	hash, _ := HashPassword("somepassword")

	if VerifyPassword("", hash) {
		t.Error("VerifyPassword should return false for empty password")
	}
}
