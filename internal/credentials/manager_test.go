package credentials

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flatrun/agent/pkg/models"
)

func setupTestManager(t *testing.T) (*Manager, string) {
	tmpDir, err := os.MkdirTemp("", "credentials-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	m := NewManager(tmpDir)
	return m, tmpDir
}

func TestNewManager(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	if m == nil {
		t.Fatal("Expected manager to be non-nil")
	}

	types := m.ListRegistryTypes()
	if len(types) == 0 {
		t.Error("Expected builtin registry types to be loaded")
	}

	var hasDockerHub bool
	for _, rt := range types {
		if rt.Slug == "docker-hub" {
			hasDockerHub = true
			break
		}
	}
	if !hasDockerHub {
		t.Error("Expected docker-hub builtin registry type")
	}
}

func TestGetRegistryType(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	rt, err := m.GetRegistryType("docker-hub")
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if rt.Name != "Docker Hub" {
		t.Errorf("Expected Docker Hub, got: %s", rt.Name)
	}

	_, err = m.GetRegistryType("nonexistent")
	if err == nil {
		t.Error("Expected error for nonexistent registry type")
	}
}

func TestCreateCredential(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	cred, err := m.CreateCredential("Test Credential", "docker-hub", "testuser", "testpass", "", false)
	if err != nil {
		t.Fatalf("Failed to create credential: %v", err)
	}

	if cred.ID == "" {
		t.Error("Expected credential ID to be set")
	}
	if cred.Name != "Test Credential" {
		t.Errorf("Expected name 'Test Credential', got: %s", cred.Name)
	}
	if cred.Username != "testuser" {
		t.Errorf("Expected username 'testuser', got: %s", cred.Username)
	}
	if cred.Password != "testpass" {
		t.Errorf("Expected password 'testpass', got: %s", cred.Password)
	}

	credsFile := filepath.Join(tmpDir, ".flatrun", "credentials.yml")
	if _, err := os.Stat(credsFile); os.IsNotExist(err) {
		t.Error("Expected credentials file to be created")
	}
}

func TestCreateCredentialDuplicateName(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	_, err := m.CreateCredential("Test Credential", "docker-hub", "user1", "pass1", "", false)
	if err != nil {
		t.Fatalf("Failed to create first credential: %v", err)
	}

	_, err = m.CreateCredential("Test Credential", "docker-hub", "user2", "pass2", "", false)
	if err == nil {
		t.Error("Expected error for duplicate credential name")
	}
}

func TestCreateCredentialInvalidRegistry(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	_, err := m.CreateCredential("Test", "nonexistent-registry", "user", "pass", "", false)
	if err == nil {
		t.Error("Expected error for invalid registry type")
	}
}

func TestGetCredential(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	created, err := m.CreateCredential("Test Cred", "docker-hub", "user", "pass", "", false)
	if err != nil {
		t.Fatalf("Failed to create credential: %v", err)
	}

	fetched, err := m.GetCredential(created.ID)
	if err != nil {
		t.Errorf("Failed to get credential: %v", err)
	}
	if fetched.Name != "Test Cred" {
		t.Errorf("Expected name 'Test Cred', got: %s", fetched.Name)
	}

	_, err = m.GetCredential("nonexistent-id")
	if err == nil {
		t.Error("Expected error for nonexistent credential")
	}
}

func TestListCredentials(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	creds := m.ListCredentials()
	if len(creds) != 0 {
		t.Errorf("Expected 0 credentials, got: %d", len(creds))
	}

	_, _ = m.CreateCredential("Cred1", "docker-hub", "user1", "pass1", "", false)
	_, _ = m.CreateCredential("Cred2", "ghcr", "user2", "pass2", "", false)

	creds = m.ListCredentials()
	if len(creds) != 2 {
		t.Errorf("Expected 2 credentials, got: %d", len(creds))
	}
}

func TestDeleteCredential(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	cred, _ := m.CreateCredential("To Delete", "docker-hub", "user", "pass", "", false)

	err := m.DeleteCredential(cred.ID)
	if err != nil {
		t.Errorf("Failed to delete credential: %v", err)
	}

	_, err = m.GetCredential(cred.ID)
	if err == nil {
		t.Error("Expected error getting deleted credential")
	}

	err = m.DeleteCredential("nonexistent")
	if err == nil {
		t.Error("Expected error deleting nonexistent credential")
	}
}

func TestUpdateCredential(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	cred, _ := m.CreateCredential("Original", "docker-hub", "user", "pass", "", false)

	updated, err := m.UpdateCredential(cred.ID, "Updated Name", "newuser", "newpass", "", nil)
	if err != nil {
		t.Fatalf("Failed to update credential: %v", err)
	}

	if updated.Name != "Updated Name" {
		t.Errorf("Expected name 'Updated Name', got: %s", updated.Name)
	}
	if updated.Username != "newuser" {
		t.Errorf("Expected username 'newuser', got: %s", updated.Username)
	}
}

func TestDefaultCredential(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	cred1, _ := m.CreateCredential("Cred1", "docker-hub", "user1", "pass1", "", true)
	cred2, _ := m.CreateCredential("Cred2", "docker-hub", "user2", "pass2", "", false)

	fetched1, _ := m.GetCredential(cred1.ID)
	if !fetched1.IsDefault {
		t.Error("Expected cred1 to be default")
	}

	isDefault := true
	_, _ = m.UpdateCredential(cred2.ID, "", "", "", "", &isDefault)

	fetched1, _ = m.GetCredential(cred1.ID)
	fetched2, _ := m.GetCredential(cred2.ID)

	if fetched1.IsDefault {
		t.Error("Expected cred1 to no longer be default")
	}
	if !fetched2.IsDefault {
		t.Error("Expected cred2 to be default")
	}
}

func TestFindCredentialForImage(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	_, _ = m.CreateCredential("Docker Hub Cred", "docker-hub", "dockeruser", "dockerpass", "", true)
	_, _ = m.CreateCredential("GHCR Cred", "ghcr", "ghcruser", "ghcrpass", "", true)

	tests := []struct {
		image        string
		expectedUser string
	}{
		{"nginx:latest", "dockeruser"},
		{"library/nginx:latest", "dockeruser"},
		{"ghcr.io/owner/repo:tag", "ghcruser"},
	}

	for _, tc := range tests {
		cred := m.FindCredentialForImage(tc.image)
		if cred == nil {
			t.Errorf("Expected credential for image %s", tc.image)
			continue
		}
		if cred.Username != tc.expectedUser {
			t.Errorf("For image %s, expected user %s, got %s", tc.image, tc.expectedUser, cred.Username)
		}
	}
}

func TestExtractRegistry(t *testing.T) {
	tests := []struct {
		image    string
		expected string
	}{
		{"nginx", "docker.io"},
		{"nginx:latest", "docker.io"},
		{"library/nginx:latest", "docker.io"},
		{"ghcr.io/owner/repo:tag", "ghcr.io"},
		{"gcr.io/project/image:tag", "gcr.io"},
		{"registry.example.com/image:tag", "registry.example.com"},
		{"registry.example.com:5000/image", "registry.example.com:5000"},
	}

	for _, tc := range tests {
		result := extractRegistry(tc.image)
		if result != tc.expected {
			t.Errorf("extractRegistry(%s) = %s, expected %s", tc.image, result, tc.expected)
		}
	}
}

func TestCreateRegistryType(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	rt, err := m.CreateRegistryType("Custom Registry", []string{"custom.io"}, models.AuthTypeBasic, "", "")
	if err != nil {
		t.Fatalf("Failed to create registry type: %v", err)
	}

	if rt.Slug != "custom-registry" {
		t.Errorf("Expected slug 'custom-registry', got: %s", rt.Slug)
	}
	if rt.Source != models.RegistrySourceLocal {
		t.Errorf("Expected source 'local', got: %s", rt.Source)
	}

	typesFile := filepath.Join(tmpDir, ".flatrun", "registry-types.yml")
	if _, err := os.Stat(typesFile); os.IsNotExist(err) {
		t.Error("Expected registry types file to be created")
	}
}

func TestDeleteBuiltinRegistryType(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	err := m.DeleteRegistryType("docker-hub")
	if err == nil {
		t.Error("Expected error when deleting builtin registry type")
	}
}

func TestPersistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "credentials-persist-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m1 := NewManager(tmpDir)
	_, err = m1.CreateCredential("Persist Test", "docker-hub", "user", "pass", "", true)
	if err != nil {
		t.Fatalf("Failed to create credential: %v", err)
	}

	m2 := NewManager(tmpDir)
	creds := m2.ListCredentials()
	if len(creds) != 1 {
		t.Fatalf("Expected 1 credential after reload, got: %d", len(creds))
	}
	if creds[0].Name != "Persist Test" {
		t.Errorf("Expected credential name 'Persist Test', got: %s", creds[0].Name)
	}
}

func TestGenerateSlug(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Docker Hub", "docker-hub"},
		{"GitHub Container Registry", "github-container-registry"},
		{"My_Custom_Registry", "my-custom-registry"},
		{"Registry123", "registry123"},
	}

	for _, tc := range tests {
		result := generateSlug(tc.input)
		if result != tc.expected {
			t.Errorf("generateSlug(%s) = %s, expected %s", tc.input, result, tc.expected)
		}
	}
}
