package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"

	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/pkg/models"
)

func setupDomainsTestServer(t *testing.T) (*Server, string, func()) {
	gin.SetMode(gin.TestMode)

	tmpDir, err := os.MkdirTemp("", "domains-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	manager := docker.NewManager(tmpDir)

	server := &Server{
		manager: manager,
	}

	cleanup := func() {
		os.RemoveAll(tmpDir)
	}

	return server, tmpDir, cleanup
}

func createTestDeployment(t *testing.T, basePath, name string, metadata *models.ServiceMetadata) {
	deployDir := filepath.Join(basePath, name)
	if err := os.MkdirAll(deployDir, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}

	composeContent := `name: ` + name + `
services:
  web:
    image: nginx:latest
`
	if err := os.WriteFile(filepath.Join(deployDir, "docker-compose.yml"), []byte(composeContent), 0644); err != nil {
		t.Fatalf("Failed to write compose file: %v", err)
	}

	if metadata != nil {
		metadataBytes, err := yaml.Marshal(metadata)
		if err != nil {
			t.Fatalf("Failed to marshal metadata: %v", err)
		}
		if err := os.WriteFile(filepath.Join(deployDir, "service.yml"), metadataBytes, 0644); err != nil {
			t.Fatalf("Failed to write service.yml: %v", err)
		}
	}
}

func TestListDomains(t *testing.T) {
	server, tmpDir, cleanup := setupDomainsTestServer(t)
	defer cleanup()

	t.Run("returns empty array when no domains", func(t *testing.T) {
		createTestDeployment(t, tmpDir, "no-domains", &models.ServiceMetadata{
			Name: "no-domains",
			Type: "web",
		})

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "name", Value: "no-domains"}}

		server.listDomains(c)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", w.Code)
		}

		var response map[string][]models.DomainConfig
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("Failed to unmarshal response: %v", err)
		}

		if len(response["domains"]) != 0 {
			t.Errorf("expected 0 domains, got %d", len(response["domains"]))
		}
	})

	t.Run("returns legacy domain with default ID", func(t *testing.T) {
		createTestDeployment(t, tmpDir, "legacy-domain", &models.ServiceMetadata{
			Name: "legacy-domain",
			Type: "web",
			Networking: models.NetworkingConfig{
				Expose:        true,
				Domain:        "legacy.example.com",
				ContainerPort: 80,
			},
			SSL: models.SSLConfig{
				Enabled:  true,
				AutoCert: true,
			},
		})

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "name", Value: "legacy-domain"}}

		server.listDomains(c)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", w.Code)
		}

		var response map[string][]models.DomainConfig
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("Failed to unmarshal response: %v", err)
		}

		if len(response["domains"]) != 1 {
			t.Fatalf("expected 1 domain, got %d", len(response["domains"]))
		}

		domain := response["domains"][0]
		if domain.ID != "default" {
			t.Errorf("expected ID 'default', got '%s'", domain.ID)
		}
		if domain.Domain != "legacy.example.com" {
			t.Errorf("expected domain 'legacy.example.com', got '%s'", domain.Domain)
		}
	})

	t.Run("returns explicit domains", func(t *testing.T) {
		createTestDeployment(t, tmpDir, "explicit-domains", &models.ServiceMetadata{
			Name: "explicit-domains",
			Type: "web",
			Domains: []models.DomainConfig{
				{
					ID:            "domain-1",
					Service:       "web",
					ContainerPort: 80,
					Domain:        "app.example.com",
				},
				{
					ID:            "domain-2",
					Service:       "api",
					ContainerPort: 8080,
					Domain:        "api.example.com",
				},
			},
		})

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "name", Value: "explicit-domains"}}

		server.listDomains(c)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", w.Code)
		}

		var response map[string][]models.DomainConfig
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("Failed to unmarshal response: %v", err)
		}

		if len(response["domains"]) != 2 {
			t.Fatalf("expected 2 domains, got %d", len(response["domains"]))
		}
	})
}

func TestDeleteDomain(t *testing.T) {
	server, tmpDir, cleanup := setupDomainsTestServer(t)
	defer cleanup()

	t.Run("deletes legacy domain with default ID", func(t *testing.T) {
		createTestDeployment(t, tmpDir, "delete-legacy", &models.ServiceMetadata{
			Name: "delete-legacy",
			Type: "web",
			Networking: models.NetworkingConfig{
				Expose:        true,
				Domain:        "legacy.example.com",
				ContainerPort: 80,
			},
			SSL: models.SSLConfig{
				Enabled:  true,
				AutoCert: true,
			},
		})

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{
			{Key: "name", Value: "delete-legacy"},
			{Key: "domainId", Value: "default"},
		}

		server.deleteDomain(c)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		// Verify the networking config was cleared
		metadataPath := filepath.Join(tmpDir, "delete-legacy", "service.yml")
		data, err := os.ReadFile(metadataPath)
		if err != nil {
			t.Fatalf("Failed to read metadata: %v", err)
		}
		var metadata models.ServiceMetadata
		if err := yaml.Unmarshal(data, &metadata); err != nil {
			t.Fatalf("Failed to unmarshal metadata: %v", err)
		}

		if metadata.Networking.Expose {
			t.Error("expected Networking.Expose to be false")
		}
		if metadata.Networking.Domain != "" {
			t.Errorf("expected Networking.Domain to be empty, got '%s'", metadata.Networking.Domain)
		}
	})

	t.Run("deletes explicit domain by ID", func(t *testing.T) {
		createTestDeployment(t, tmpDir, "delete-explicit", &models.ServiceMetadata{
			Name: "delete-explicit",
			Type: "web",
			Domains: []models.DomainConfig{
				{
					ID:            "domain-1",
					Service:       "web",
					ContainerPort: 80,
					Domain:        "app.example.com",
				},
				{
					ID:            "domain-2",
					Service:       "api",
					ContainerPort: 8080,
					Domain:        "api.example.com",
				},
			},
		})

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{
			{Key: "name", Value: "delete-explicit"},
			{Key: "domainId", Value: "domain-1"},
		}

		server.deleteDomain(c)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		// Verify only domain-2 remains
		metadataPath := filepath.Join(tmpDir, "delete-explicit", "service.yml")
		data, err := os.ReadFile(metadataPath)
		if err != nil {
			t.Fatalf("Failed to read metadata: %v", err)
		}
		var metadata models.ServiceMetadata
		if err := yaml.Unmarshal(data, &metadata); err != nil {
			t.Fatalf("Failed to unmarshal metadata: %v", err)
		}

		if len(metadata.Domains) != 1 {
			t.Fatalf("expected 1 domain remaining, got %d", len(metadata.Domains))
		}
		if metadata.Domains[0].ID != "domain-2" {
			t.Errorf("expected remaining domain ID 'domain-2', got '%s'", metadata.Domains[0].ID)
		}
	})

	t.Run("returns 404 for non-existent domain", func(t *testing.T) {
		createTestDeployment(t, tmpDir, "no-such-domain", &models.ServiceMetadata{
			Name: "no-such-domain",
			Type: "web",
			Domains: []models.DomainConfig{
				{
					ID:     "domain-1",
					Domain: "app.example.com",
				},
			},
		})

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{
			{Key: "name", Value: "no-such-domain"},
			{Key: "domainId", Value: "non-existent"},
		}

		server.deleteDomain(c)

		if w.Code != http.StatusNotFound {
			t.Errorf("expected status 404, got %d", w.Code)
		}
	})

	t.Run("returns 404 for default ID when no legacy domain", func(t *testing.T) {
		createTestDeployment(t, tmpDir, "no-legacy", &models.ServiceMetadata{
			Name: "no-legacy",
			Type: "web",
			Networking: models.NetworkingConfig{
				Expose: false,
				Domain: "",
			},
		})

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{
			{Key: "name", Value: "no-legacy"},
			{Key: "domainId", Value: "default"},
		}

		server.deleteDomain(c)

		if w.Code != http.StatusNotFound {
			t.Errorf("expected status 404, got %d", w.Code)
		}
	})
}

func TestAddDomain(t *testing.T) {
	server, tmpDir, cleanup := setupDomainsTestServer(t)
	defer cleanup()

	t.Run("adds domain to deployment", func(t *testing.T) {
		createTestDeployment(t, tmpDir, "add-domain", &models.ServiceMetadata{
			Name: "add-domain",
			Type: "web",
		})

		newDomain := models.DomainConfig{
			Service:       "web",
			ContainerPort: 80,
			Domain:        "new.example.com",
			SSL: models.SSLConfig{
				Enabled:  true,
				AutoCert: true,
			},
		}
		body, _ := json.Marshal(newDomain)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "name", Value: "add-domain"}}
		c.Request = httptest.NewRequest("POST", "/", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		server.addDomain(c)

		// Check metadata was saved (proxy setup may fail without orchestrator)
		metadataPath := filepath.Join(tmpDir, "add-domain", "service.yml")
		data, err := os.ReadFile(metadataPath)
		if err != nil {
			t.Fatalf("Failed to read metadata: %v", err)
		}
		var metadata models.ServiceMetadata
		if err := yaml.Unmarshal(data, &metadata); err != nil {
			t.Fatalf("Failed to unmarshal metadata: %v", err)
		}

		if len(metadata.Domains) != 1 {
			t.Fatalf("expected 1 domain, got %d", len(metadata.Domains))
		}
		if metadata.Domains[0].Domain != "new.example.com" {
			t.Errorf("expected domain 'new.example.com', got '%s'", metadata.Domains[0].Domain)
		}
		if metadata.Domains[0].ID == "" {
			t.Error("expected domain ID to be generated")
		}
	})

	t.Run("rejects duplicate domain", func(t *testing.T) {
		createTestDeployment(t, tmpDir, "duplicate-domain", &models.ServiceMetadata{
			Name: "duplicate-domain",
			Type: "web",
			Domains: []models.DomainConfig{
				{
					ID:            "existing-1",
					Service:       "web",
					ContainerPort: 80,
					Domain:        "existing.example.com",
				},
			},
		})

		duplicateDomain := models.DomainConfig{
			Service:       "web",
			ContainerPort: 8080,
			Domain:        "existing.example.com",
		}
		body, _ := json.Marshal(duplicateDomain)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "name", Value: "duplicate-domain"}}
		c.Request = httptest.NewRequest("POST", "/", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		server.addDomain(c)

		if w.Code != http.StatusConflict {
			t.Errorf("expected status 409 Conflict, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("allows same domain with different path prefix", func(t *testing.T) {
		createTestDeployment(t, tmpDir, "different-path", &models.ServiceMetadata{
			Name: "different-path",
			Type: "web",
			Domains: []models.DomainConfig{
				{
					ID:            "existing-1",
					Service:       "api",
					ContainerPort: 8080,
					Domain:        "app.example.com",
					PathPrefix:    "/api",
				},
			},
		})

		newDomain := models.DomainConfig{
			Service:       "web",
			ContainerPort: 80,
			Domain:        "app.example.com",
			PathPrefix:    "/web",
		}
		body, _ := json.Marshal(newDomain)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "name", Value: "different-path"}}
		c.Request = httptest.NewRequest("POST", "/", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		server.addDomain(c)

		if w.Code != http.StatusCreated {
			t.Errorf("expected status 201 Created, got %d: %s", w.Code, w.Body.String())
		}
	})
}
