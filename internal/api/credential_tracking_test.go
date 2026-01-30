package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/models"
	"gopkg.in/yaml.v3"
)

func TestServiceMetadataCredentialID(t *testing.T) {
	metadata := &models.ServiceMetadata{
		Name: "test-deployment",
		Type: "custom",
		Networking: models.NetworkingConfig{
			Expose:        true,
			Domain:        "test.example.com",
			ContainerPort: 8080,
		},
		CredentialID: "cred-123",
	}

	if metadata.CredentialID != "cred-123" {
		t.Errorf("expected CredentialID 'cred-123', got '%s'", metadata.CredentialID)
	}
}

func TestServiceMetadataCredentialIDEmpty(t *testing.T) {
	metadata := &models.ServiceMetadata{
		Name: "public-deployment",
		Type: "custom",
	}

	if metadata.CredentialID != "" {
		t.Errorf("expected empty CredentialID for public deployment, got '%s'", metadata.CredentialID)
	}
}

func TestServiceMetadataCredentialIDOmitempty(t *testing.T) {
	metadata := &models.ServiceMetadata{
		Name: "test",
		Type: "custom",
	}

	if metadata.CredentialID != "" {
		t.Error("CredentialID should be empty by default")
	}

	metadata.CredentialID = "new-cred-id"
	if metadata.CredentialID != "new-cred-id" {
		t.Errorf("expected CredentialID 'new-cred-id', got '%s'", metadata.CredentialID)
	}
}

func TestServiceMetadataCredentialIDYAMLSerialization(t *testing.T) {
	metadata := &models.ServiceMetadata{
		Name:         "private-app",
		Type:         "custom",
		CredentialID: "cred-abc-123",
	}

	data, err := yaml.Marshal(metadata)
	if err != nil {
		t.Fatalf("failed to marshal metadata: %v", err)
	}

	yamlStr := string(data)
	if !strings.Contains(yamlStr, "credential_id: cred-abc-123") {
		t.Errorf("YAML should contain credential_id field, got:\n%s", yamlStr)
	}

	var unmarshaled models.ServiceMetadata
	if err := yaml.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("failed to unmarshal metadata: %v", err)
	}

	if unmarshaled.CredentialID != "cred-abc-123" {
		t.Errorf("expected CredentialID 'cred-abc-123' after unmarshal, got '%s'", unmarshaled.CredentialID)
	}
}

func TestServiceMetadataCredentialIDYAMLOmitsEmpty(t *testing.T) {
	metadata := &models.ServiceMetadata{
		Name: "public-app",
		Type: "custom",
	}

	data, err := yaml.Marshal(metadata)
	if err != nil {
		t.Fatalf("failed to marshal metadata: %v", err)
	}

	yamlStr := string(data)
	if strings.Contains(yamlStr, "credential_id") {
		t.Errorf("YAML should omit credential_id when empty, got:\n%s", yamlStr)
	}
}

func TestServiceMetadataCredentialIDJSONSerialization(t *testing.T) {
	metadata := &models.ServiceMetadata{
		Name:         "private-app",
		Type:         "custom",
		CredentialID: "cred-xyz-789",
	}

	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("failed to marshal metadata: %v", err)
	}

	jsonStr := string(data)
	if !strings.Contains(jsonStr, `"credential_id":"cred-xyz-789"`) {
		t.Errorf("JSON should contain credential_id field, got:\n%s", jsonStr)
	}

	var unmarshaled models.ServiceMetadata
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("failed to unmarshal metadata: %v", err)
	}

	if unmarshaled.CredentialID != "cred-xyz-789" {
		t.Errorf("expected CredentialID 'cred-xyz-789' after unmarshal, got '%s'", unmarshaled.CredentialID)
	}
}

func TestServiceMetadataCredentialIDJSONOmitsEmpty(t *testing.T) {
	metadata := &models.ServiceMetadata{
		Name: "public-app",
		Type: "custom",
	}

	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("failed to marshal metadata: %v", err)
	}

	jsonStr := string(data)
	if strings.Contains(jsonStr, "credential_id") {
		t.Errorf("JSON should omit credential_id when empty, got:\n%s", jsonStr)
	}
}
