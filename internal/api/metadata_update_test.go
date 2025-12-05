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
