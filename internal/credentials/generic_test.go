package credentials

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/models"
)

func TestGenericCredential_CreateGetUpdateDelete(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	cred, err := m.CreateGenericCredential("prod-s3", models.CredentialKindS3, map[string]string{
		"access_key_id":     "AKIAEXAMPLE",
		"secret_access_key": "supersecret",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := m.GetGenericCredential(cred.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Data["secret_access_key"] != "supersecret" {
		t.Fatalf("expected raw secret from GetGenericCredential, got %q", got.Data["secret_access_key"])
	}

	// A masked or empty secret on update leaves the stored secret untouched,
	// while other fields still update.
	updated, err := m.UpdateGenericCredential(cred.ID, "prod-s3-renamed", map[string]string{
		"access_key_id":     "AKIANEW",
		"secret_access_key": models.CredentialMask,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "prod-s3-renamed" {
		t.Fatalf("expected rename, got %q", updated.Name)
	}
	if updated.Data["access_key_id"] != "AKIANEW" {
		t.Fatalf("expected access key updated, got %q", updated.Data["access_key_id"])
	}
	if updated.Data["secret_access_key"] != "supersecret" {
		t.Fatalf("expected secret preserved through mask, got %q", updated.Data["secret_access_key"])
	}

	// A real new secret replaces the old one.
	rotated, err := m.UpdateGenericCredential(cred.ID, "", map[string]string{"secret_access_key": "rotated"})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated.Data["secret_access_key"] != "rotated" {
		t.Fatalf("expected rotated secret, got %q", rotated.Data["secret_access_key"])
	}

	if err := m.DeleteGenericCredential(cred.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.GetGenericCredential(cred.ID); err == nil {
		t.Fatal("expected credential gone after delete")
	}
}

func TestGenericCredential_RejectsUnknownKind(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	if _, err := m.CreateGenericCredential("x", models.CredentialKind("ftp"), nil); err == nil {
		t.Fatal("expected unknown kind to be rejected")
	}
}

func TestGenericCredential_PersistsAcrossReload(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	if _, err := m.CreateGenericCredential("prod", models.CredentialKindS3, map[string]string{
		"access_key_id":     "AKIA",
		"secret_access_key": "s3cr3t",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	reloaded := NewManager(tmpDir)
	list := reloaded.ListGenericCredentials(models.CredentialKindS3)
	if len(list) != 1 || list[0].Name != "prod" {
		t.Fatalf("expected persisted credential after reload, got %#v", list)
	}
}

func TestGenericCredential_JSONMasksSecret(t *testing.T) {
	cred := models.Credential{
		Name: "prod",
		Kind: models.CredentialKindS3,
		Data: map[string]string{
			"access_key_id":     "AKIAVISIBLE",
			"secret_access_key": "hidden",
		},
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	if strings.Contains(s, "hidden") {
		t.Fatalf("secret leaked into JSON: %s", s)
	}
	if !strings.Contains(s, "AKIAVISIBLE") {
		t.Fatalf("access key id should be visible: %s", s)
	}
	if !strings.Contains(s, models.CredentialMask) {
		t.Fatalf("expected mask sentinel in JSON: %s", s)
	}
}
