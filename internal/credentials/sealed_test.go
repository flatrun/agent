package credentials

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flatrun/agent/internal/secrets"
	"github.com/flatrun/agent/pkg/models"
)

func TestSealerSealsStoredSecretsAndKeepsThemReadable(t *testing.T) {
	dir := t.TempDir()
	plain := NewManager(dir)
	cred, err := plain.CreateGenericCredential("vault-prod", models.CredentialKindVault, map[string]string{"address": "https://vault.example.com", "token": "s.plaintoken"})
	if err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(dir, ".flatrun", "credentials-store.yml")
	if raw, _ := os.ReadFile(storePath); !strings.Contains(string(raw), "s.plaintoken") {
		t.Fatalf("expected the pre-encryption store to be plain: %s", raw)
	}

	keyring, err := secrets.LoadKeyring(filepath.Join(dir, ".flatrun", "secrets.key"))
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(dir)
	if err := m.SetSealer(keyring); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(storePath)
	if strings.Contains(string(raw), "s.plaintoken") || !strings.Contains(string(raw), "https://vault.example.com") {
		t.Fatalf("store after sealing: %s", raw)
	}

	reopened := NewManager(dir)
	if err := reopened.SetSealer(keyring); err != nil {
		t.Fatal(err)
	}
	got, err := reopened.GetGenericCredential(cred.ID)
	if err != nil || got.Data["token"] != "s.plaintoken" {
		t.Fatalf("reopened credential = %+v, %v", got, err)
	}
	if _, err := reopened.UpdateGenericCredential(cred.ID, "", map[string]string{"token": "s.second"}); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(storePath); strings.Contains(string(raw), "s.second") {
		t.Fatalf("updated secret written plain: %s", raw)
	}
}

func TestUnreadableCredentialKeepsTheStoreOnDisk(t *testing.T) {
	dir := t.TempDir()
	keyring, err := secrets.LoadKeyring(filepath.Join(dir, ".flatrun", "secrets.key"))
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(dir)
	if err := m.SetSealer(keyring); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"vault-a", "vault-b"} {
		if _, err := m.CreateGenericCredential(name, models.CredentialKindVault, map[string]string{"address": "https://vault.example.com", "token": "s." + name}); err != nil {
			t.Fatal(err)
		}
	}

	otherKeyring, err := secrets.LoadKeyring(filepath.Join(t.TempDir(), "other.key"))
	if err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(dir, ".flatrun", "credentials-store.yml")
	before, _ := os.ReadFile(storePath)

	reopened := NewManager(dir)
	if err := reopened.SetSealer(otherKeyring); err == nil {
		t.Fatal("expected opening the store with the wrong key to fail")
	}
	if _, err := reopened.CreateGenericCredential("vault-c", models.CredentialKindVault, map[string]string{"token": "s.c"}); err == nil {
		t.Fatal("expected saving to be refused while stored credentials are unreadable")
	}
	if after, _ := os.ReadFile(storePath); string(after) != string(before) {
		t.Fatalf("store changed on disk:\nbefore: %s\nafter: %s", before, after)
	}
}
