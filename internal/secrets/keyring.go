// Package secrets keeps secret values encrypted at rest and fetches secrets
// from external managers such as Infisical, Vault, Doppler and 1Password.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// sealedPrefix marks a sealed value and names the format, so a later format
// can be told apart and a plain value is never mistaken for a sealed one.
const sealedPrefix = "enc:v1:"

var ErrNotSealed = errors.New("value is not sealed")

// Keyring seals values with AES-256-GCM under a key kept in its own file.
type Keyring struct {
	aead cipher.AEAD
}

// LoadKeyring reads the key at path, creating a new random key when the file
// does not exist.
func LoadKeyring(path string) (*Keyring, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		encoded := []byte(hex.EncodeToString(key) + "\n")
		if err := os.WriteFile(path, encoded, 0600); err != nil {
			return nil, err
		}
		return NewKeyring(key)
	}
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("secrets key %s is not hex: %w", path, err)
	}
	return NewKeyring(key)
}

func NewKeyring(key []byte) (*Keyring, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secrets key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Keyring{aead: aead}, nil
}

func IsSealed(value string) bool {
	return strings.HasPrefix(value, sealedPrefix)
}

func (k *Keyring) Seal(plain string) (string, error) {
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := k.aead.Seal(nonce, nonce, []byte(plain), nil)
	return sealedPrefix + base64.StdEncoding.EncodeToString(out), nil
}

func (k *Keyring) Open(sealed string) (string, error) {
	if !IsSealed(sealed) {
		return "", ErrNotSealed
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, sealedPrefix))
	if err != nil {
		return "", err
	}
	size := k.aead.NonceSize()
	if len(raw) < size {
		return "", errors.New("sealed value is truncated")
	}
	plain, err := k.aead.Open(nil, raw[:size], raw[size:], nil)
	if err != nil {
		return "", fmt.Errorf("cannot open sealed value: %w", err)
	}
	return string(plain), nil
}
