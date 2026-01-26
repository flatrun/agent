package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

const (
	apiKeyLength = 32
	keyIDLength  = 12
	keyPrefix    = "fr_"
)

func GenerateAPIKey() (plainKey string, keyHash string, keyID string, prefix string, err error) {
	keyBytes := make([]byte, apiKeyLength)
	if _, err := rand.Read(keyBytes); err != nil {
		return "", "", "", "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	plainKey = keyPrefix + base64.RawURLEncoding.EncodeToString(keyBytes)

	hash := sha256.Sum256([]byte(plainKey))
	keyHash = hex.EncodeToString(hash[:])

	idBytes := make([]byte, keyIDLength/2)
	if _, err := rand.Read(idBytes); err != nil {
		return "", "", "", "", fmt.Errorf("failed to generate key ID: %w", err)
	}
	keyID = hex.EncodeToString(idBytes)

	if len(plainKey) >= 12 {
		prefix = plainKey[:12] + "..."
	} else {
		prefix = plainKey + "..."
	}

	return plainKey, keyHash, keyID, prefix, nil
}

func HashAPIKey(key string) string {
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:])
}

func GenerateUID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func GenerateSessionID() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
