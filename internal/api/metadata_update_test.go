package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/flatrun/agent/internal/proxy"
	"github.com/flatrun/agent/pkg/models"
)

func parseTestJSON(t *testing.T, jsonStr string) (map[string]json.RawMessage, models.ServiceMetadata) {
	t.Helper()
	var sentFields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonStr), &sentFields); err != nil {
		t.Fatalf("failed to parse sentFields: %v", err)
	}
	var incoming models.ServiceMetadata
	if err := json.Unmarshal([]byte(jsonStr), &incoming); err != nil {
		t.Fatalf("failed to parse incoming: %v", err)
	}
	return sentFields, incoming
}

func TestMergeMetadata_PartialUpdatePreservesOtherFields(t *testing.T) {
	existing := &models.ServiceMetadata{
		Name: "wordpress-app",
		Type: "wordpress",
		Networking: models.NetworkingConfig{
			Expose:        true,
			Domain:        "blog.example.com",
			ContainerPort: 80,
		},
		SSL: models.SSLConfig{
			Enabled:  true,
			AutoCert: true,
		},
		QuickActions: []models.QuickAction{
			{ID: "clear-cache", Name: "Clear Cache", Command: "wp cache flush"},
		},
		Security: &models.DeploymentSecurityConfig{
			Enabled: true,
			ProtectedPaths: []models.ProtectedPath{
				{Pattern: "/.env", Enabled: true},
			},
		},
	}

	sentFields, incoming := parseTestJSON(t, `{"credential_id": "cred-123"}`)

	merged := mergeMetadata(existing, &incoming, sentFields)

	if merged.CredentialID != "cred-123" {
		t.Errorf("sent field credential_id should be updated: expected 'cred-123', got '%s'", merged.CredentialID)
	}

	if merged.Name != "wordpress-app" {
		t.Errorf("unsent field Name should be preserved: expected 'wordpress-app', got '%s'", merged.Name)
	}
	if merged.Type != "wordpress" {
		t.Errorf("unsent field Type should be preserved: expected 'wordpress', got '%s'", merged.Type)
	}
	if !merged.Networking.Expose || merged.Networking.Domain != "blog.example.com" {
		t.Error("unsent field Networking should be preserved")
	}
	if !merged.SSL.Enabled || !merged.SSL.AutoCert {
		t.Error("unsent field SSL should be preserved")
	}
	if len(merged.QuickActions) != 1 || merged.QuickActions[0].ID != "clear-cache" {
		t.Error("unsent field QuickActions should be preserved")
	}
	if merged.Security == nil || !merged.Security.Enabled {
		t.Error("unsent field Security should be preserved")
	}
}

func TestMergeMetadata_SentFieldOverwritesExisting(t *testing.T) {
	existing := &models.ServiceMetadata{
		Name:         "old-name",
		Type:         "custom",
		CredentialID: "old-cred",
		Networking: models.NetworkingConfig{
			Expose:        true,
			Domain:        "old.example.com",
			ContainerPort: 80,
		},
	}

	sentFields, incoming := parseTestJSON(t, `{
		"networking": {
			"expose": true,
			"domain": "new.example.com",
			"container_port": 8080,
			"protocol": "http",
			"proxy_type": "http"
		}
	}`)

	merged := mergeMetadata(existing, &incoming, sentFields)

	if merged.Networking.Domain != "new.example.com" {
		t.Errorf("sent field Networking.Domain should be updated: expected 'new.example.com', got '%s'", merged.Networking.Domain)
	}
	if merged.Networking.ContainerPort != 8080 {
		t.Errorf("sent field Networking.ContainerPort should be updated: expected 8080, got %d", merged.Networking.ContainerPort)
	}

	if merged.CredentialID != "old-cred" {
		t.Errorf("unsent field CredentialID should be preserved: expected 'old-cred', got '%s'", merged.CredentialID)
	}
	if merged.Name != "old-name" {
		t.Errorf("unsent field Name should be preserved: expected 'old-name', got '%s'", merged.Name)
	}
}

func TestMergeMetadata_CanSetFieldToFalseOrEmpty(t *testing.T) {
	existing := &models.ServiceMetadata{
		Name:         "test-app",
		CredentialID: "has-credential",
		Networking: models.NetworkingConfig{
			Expose: true,
			Domain: "test.example.com",
		},
		SSL: models.SSLConfig{
			Enabled:  true,
			AutoCert: true,
		},
	}

	sentFields, incoming := parseTestJSON(t, `{
		"credential_id": "",
		"networking": {"expose": false, "domain": "", "container_port": 0, "protocol": "", "proxy_type": ""},
		"ssl": {"enabled": false, "auto_cert": false}
	}`)

	merged := mergeMetadata(existing, &incoming, sentFields)

	if merged.CredentialID != "" {
		t.Errorf("credential_id should be cleared when explicitly sent as empty, got '%s'", merged.CredentialID)
	}
	if merged.Networking.Expose {
		t.Error("Networking.Expose should be set to false when explicitly sent")
	}
	if merged.SSL.Enabled {
		t.Error("SSL.Enabled should be set to false when explicitly sent")
	}

	if merged.Name != "test-app" {
		t.Errorf("unsent field Name should be preserved, got '%s'", merged.Name)
	}
}

func TestMergeMetadata_MultipleFieldsUpdated(t *testing.T) {
	existing := &models.ServiceMetadata{
		Name: "app",
		Type: "custom",
		Networking: models.NetworkingConfig{
			Expose: false,
		},
		QuickActions: []models.QuickAction{
			{ID: "action1", Name: "Action 1"},
		},
	}

	sentFields, incoming := parseTestJSON(t, `{
		"name": "updated-app",
		"type": "wordpress",
		"credential_id": "new-cred"
	}`)

	merged := mergeMetadata(existing, &incoming, sentFields)

	if merged.Name != "updated-app" {
		t.Errorf("Name should be updated, got '%s'", merged.Name)
	}
	if merged.Type != "wordpress" {
		t.Errorf("Type should be updated, got '%s'", merged.Type)
	}
	if merged.CredentialID != "new-cred" {
		t.Errorf("CredentialID should be updated, got '%s'", merged.CredentialID)
	}

	if merged.Networking.Expose {
		t.Error("Networking should be preserved (not sent)")
	}
	if len(merged.QuickActions) != 1 {
		t.Error("QuickActions should be preserved (not sent)")
	}
}

func TestMergeMetadata_NilExisting(t *testing.T) {
	sentFields, incoming := parseTestJSON(t, `{"name": "new-app", "credential_id": "cred-123"}`)

	merged := mergeMetadata(nil, &incoming, sentFields)

	if merged.Name != "new-app" || merged.CredentialID != "cred-123" {
		t.Error("when existing is nil, incoming should be returned as-is")
	}
}

func TestMergeMetadata_NilIncoming(t *testing.T) {
	existing := &models.ServiceMetadata{
		Name: "existing-app",
		Type: "custom",
	}

	merged := mergeMetadata(existing, nil, nil)

	if merged != existing {
		t.Error("when incoming is nil, existing should be returned as-is")
	}
}

type mockManager struct {
	deployment *models.Deployment
	savedMeta  *models.ServiceMetadata
}

func (m *mockManager) GetDeployment(name string) (*models.Deployment, error) {
	return m.deployment, nil
}

func (m *mockManager) SaveMetadata(name string, metadata *models.ServiceMetadata) error {
	m.savedMeta = metadata
	return nil
}

type mockProxyOrchestrator struct {
	setupCalledWith *models.Deployment
	teardownCalled  bool
}

func (m *mockProxyOrchestrator) SetupDeployment(deployment *models.Deployment) (*proxy.SetupResult, error) {
	m.setupCalledWith = deployment
	return &proxy.SetupResult{Success: true}, nil
}

func (m *mockProxyOrchestrator) TeardownDeployment(name string) error {
	m.teardownCalled = true
	return nil
}

func TestUpdateDeploymentMetadata_DomainChange(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		oldDomain      string
		newDomain      string
		expectSetup    bool
		expectTeardown bool
	}{
		{
			name:        "domain change triggers proxy setup with new domain",
			oldDomain:   "old.example.com",
			newDomain:   "new.example.com",
			expectSetup: true,
		},
		{
			name:        "same domain still triggers proxy setup",
			oldDomain:   "same.example.com",
			newDomain:   "same.example.com",
			expectSetup: true,
		},
		{
			name:           "expose disabled triggers teardown",
			oldDomain:      "old.example.com",
			newDomain:      "",
			expectTeardown: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockMgr := &mockManager{
				deployment: &models.Deployment{
					Name: "test-app",
					Metadata: &models.ServiceMetadata{
						Networking: models.NetworkingConfig{
							Expose: true,
							Domain: tt.oldDomain,
						},
					},
				},
			}

			mockProxy := &mockProxyOrchestrator{}

			handler := createUpdateMetadataHandler(mockMgr, mockProxy)

			expose := tt.newDomain != ""
			metadata := models.ServiceMetadata{
				Name: "test-app",
				Type: "custom",
				Networking: models.NetworkingConfig{
					Expose:        expose,
					Domain:        tt.newDomain,
					ContainerPort: 80,
				},
				SSL: models.SSLConfig{
					Enabled:  true,
					AutoCert: true,
				},
			}

			body, _ := json.Marshal(metadata)
			req := httptest.NewRequest("PUT", "/deployments/test-app/metadata", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			router := gin.New()
			router.PUT("/deployments/:name/metadata", handler)
			router.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
			}

			if tt.expectSetup {
				if mockProxy.setupCalledWith == nil {
					t.Fatal("expected SetupDeployment to be called")
				}
				if mockProxy.setupCalledWith.Metadata.Networking.Domain != tt.newDomain {
					t.Errorf("expected SetupDeployment with domain %q, got %q",
						tt.newDomain, mockProxy.setupCalledWith.Metadata.Networking.Domain)
				}
			}

			if tt.expectTeardown {
				if !mockProxy.teardownCalled {
					t.Error("expected TeardownDeployment to be called")
				}
			}
		})
	}
}

type deploymentManager interface {
	GetDeployment(name string) (*models.Deployment, error)
	SaveMetadata(name string, metadata *models.ServiceMetadata) error
}

type proxySetupOrchestrator interface {
	SetupDeployment(deployment *models.Deployment) (*proxy.SetupResult, error)
	TeardownDeployment(name string) error
}

func createUpdateMetadataHandler(mgr deploymentManager, proxyOrch proxySetupOrchestrator) gin.HandlerFunc {
	return func(c *gin.Context) {
		name := c.Param("name")

		var metadata models.ServiceMetadata
		if err := c.ShouldBindJSON(&metadata); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		deployment, err := mgr.GetDeployment(name)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
			return
		}

		if err := mgr.SaveMetadata(name, &metadata); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		deployment.Metadata = &metadata

		var proxyResult *proxy.SetupResult
		if metadata.Networking.Expose {
			proxyResult, _ = proxyOrch.SetupDeployment(deployment)
		} else {
			_ = proxyOrch.TeardownDeployment(name)
		}

		c.JSON(http.StatusOK, gin.H{
			"message":      "Metadata updated",
			"name":         name,
			"proxy_result": proxyResult,
		})
	}
}
