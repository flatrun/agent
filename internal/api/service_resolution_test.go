package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/pkg/models"
	"gopkg.in/yaml.v3"
)

func setupServiceResolutionServer(t *testing.T) (*Server, string, func()) {
	gin.SetMode(gin.TestMode)

	tmpDir, err := os.MkdirTemp("", "svc-resolution-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	manager := docker.NewManager(tmpDir)
	server := &Server{manager: manager}

	cleanup := func() { os.RemoveAll(tmpDir) }
	return server, tmpDir, cleanup
}

func createDeploymentWithServices(t *testing.T, basePath, name, composeContent string, metadata *models.ServiceMetadata) {
	deployDir := filepath.Join(basePath, name)
	if err := os.MkdirAll(deployDir, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deployDir, "docker-compose.yml"), []byte(composeContent), 0644); err != nil {
		t.Fatalf("Failed to write compose file: %v", err)
	}
	if metadata != nil {
		data, err := yaml.Marshal(metadata)
		if err != nil {
			t.Fatalf("Failed to marshal metadata: %v", err)
		}
		if err := os.WriteFile(filepath.Join(deployDir, "service.yml"), data, 0644); err != nil {
			t.Fatalf("Failed to write service.yml: %v", err)
		}
	}
}

func TestResolveService(t *testing.T) {
	server, tmpDir, cleanup := setupServiceResolutionServer(t)
	defer cleanup()

	t.Run("single service auto-resolves", func(t *testing.T) {
		createDeploymentWithServices(t, tmpDir, "single-svc", `name: single-svc
services:
  backend:
    image: myapp:latest
`, nil)

		resolved, err := server.resolveService("single-svc", "")
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}
		if resolved != "backend" {
			t.Errorf("Expected 'backend', got %q", resolved)
		}
	})

	t.Run("single service with explicit name validates", func(t *testing.T) {
		createDeploymentWithServices(t, tmpDir, "single-explicit", `name: single-explicit
services:
  api:
    image: myapp:latest
`, nil)

		resolved, err := server.resolveService("single-explicit", "api")
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}
		if resolved != "api" {
			t.Errorf("Expected 'api', got %q", resolved)
		}
	})

	t.Run("single service rejects invalid service name", func(t *testing.T) {
		createDeploymentWithServices(t, tmpDir, "single-invalid", `name: single-invalid
services:
  web:
    image: nginx:latest
`, nil)

		_, err := server.resolveService("single-invalid", "nonexistent")
		if err == nil {
			t.Fatal("Expected error for invalid service name")
		}
		if !svcContains(err.Error(), "not found") {
			t.Errorf("Error should mention 'not found', got: %v", err)
		}
		if !svcContains(err.Error(), "web") {
			t.Errorf("Error should list available service 'web', got: %v", err)
		}
	})

	t.Run("multiple services with app defaults to app", func(t *testing.T) {
		createDeploymentWithServices(t, tmpDir, "multi-app", `name: multi-app
services:
  app:
    image: myapp:latest
  db:
    image: postgres:15
  cache:
    image: redis:alpine
`, nil)

		resolved, err := server.resolveService("multi-app", "")
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}
		if resolved != "app" {
			t.Errorf("Expected 'app', got %q", resolved)
		}
	})

	t.Run("multiple services without app rejects", func(t *testing.T) {
		createDeploymentWithServices(t, tmpDir, "multi-no-app", `name: multi-no-app
services:
  web:
    image: nginx:latest
  api:
    image: myapi:latest
  worker:
    image: myworker:latest
`, nil)

		_, err := server.resolveService("multi-no-app", "")
		if err == nil {
			t.Fatal("Expected error when multiple services and no 'app'")
		}
		if !svcContains(err.Error(), "multiple services found") {
			t.Errorf("Error should mention 'multiple services found', got: %v", err)
		}
		if !svcContains(err.Error(), "web") || !svcContains(err.Error(), "api") || !svcContains(err.Error(), "worker") {
			t.Errorf("Error should list all available services, got: %v", err)
		}
	})

	t.Run("multiple services with explicit valid name resolves", func(t *testing.T) {
		createDeploymentWithServices(t, tmpDir, "multi-explicit", `name: multi-explicit
services:
  frontend:
    image: nginx:latest
  api:
    image: myapi:latest
`, nil)

		resolved, err := server.resolveService("multi-explicit", "api")
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}
		if resolved != "api" {
			t.Errorf("Expected 'api', got %q", resolved)
		}
	})

	t.Run("nonexistent deployment returns error", func(t *testing.T) {
		_, err := server.resolveService("does-not-exist", "")
		if err == nil {
			t.Fatal("Expected error for nonexistent deployment")
		}
	})
}

func TestAddDomainWithServiceResolution(t *testing.T) {
	server, tmpDir, cleanup := setupServiceResolutionServer(t)
	defer cleanup()

	t.Run("auto-resolves service for single-service deployment", func(t *testing.T) {
		createDeploymentWithServices(t, tmpDir, "single-add", `name: single-add
services:
  backend:
    image: myapp:latest
`, &models.ServiceMetadata{
			Name: "single-add",
			Type: "web",
		})

		domain := models.DomainConfig{
			Domain:        "single.example.com",
			ContainerPort: 3000,
		}
		body, _ := json.Marshal(domain)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "name", Value: "single-add"}}
		c.Request = httptest.NewRequest("POST", "/", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		server.addDomain(c)

		metadataPath := filepath.Join(tmpDir, "single-add", "service.yml")
		data, err := os.ReadFile(metadataPath)
		if err != nil {
			t.Fatalf("Failed to read metadata: %v", err)
		}
		var metadata models.ServiceMetadata
		if err := yaml.Unmarshal(data, &metadata); err != nil {
			t.Fatalf("Failed to unmarshal metadata: %v", err)
		}

		if len(metadata.Domains) != 1 {
			t.Fatalf("Expected 1 domain, got %d", len(metadata.Domains))
		}
		if metadata.Domains[0].Service != "backend" {
			t.Errorf("Expected service 'backend', got %q", metadata.Domains[0].Service)
		}
	})

	t.Run("rejects invalid service for multi-service deployment", func(t *testing.T) {
		createDeploymentWithServices(t, tmpDir, "multi-invalid-svc", `name: multi-invalid-svc
services:
  web:
    image: nginx:latest
  api:
    image: myapi:latest
`, &models.ServiceMetadata{
			Name: "multi-invalid-svc",
			Type: "web",
		})

		domain := models.DomainConfig{
			Service:       "nonexistent",
			Domain:        "invalid.example.com",
			ContainerPort: 80,
		}
		body, _ := json.Marshal(domain)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "name", Value: "multi-invalid-svc"}}
		c.Request = httptest.NewRequest("POST", "/", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		server.addDomain(c)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected status 400, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("rejects when multiple services and no app and no service specified", func(t *testing.T) {
		createDeploymentWithServices(t, tmpDir, "multi-ambiguous", `name: multi-ambiguous
services:
  frontend:
    image: nginx:latest
  backend:
    image: myapp:latest
`, &models.ServiceMetadata{
			Name: "multi-ambiguous",
			Type: "web",
		})

		domain := models.DomainConfig{
			Domain:        "ambiguous.example.com",
			ContainerPort: 80,
		}
		body, _ := json.Marshal(domain)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "name", Value: "multi-ambiguous"}}
		c.Request = httptest.NewRequest("POST", "/", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		server.addDomain(c)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected status 400, got %d: %s", w.Code, w.Body.String())
		}
		if !svcContains(w.Body.String(), "multiple services found") {
			t.Errorf("Expected error about multiple services, got: %s", w.Body.String())
		}
	})

	t.Run("resolves app service for multi-service deployment", func(t *testing.T) {
		createDeploymentWithServices(t, tmpDir, "multi-app-add", `name: multi-app-add
services:
  app:
    image: myapp:latest
  db:
    image: postgres:15
`, &models.ServiceMetadata{
			Name: "multi-app-add",
			Type: "web",
		})

		domain := models.DomainConfig{
			Domain:        "multiapp.example.com",
			ContainerPort: 8080,
		}
		body, _ := json.Marshal(domain)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "name", Value: "multi-app-add"}}
		c.Request = httptest.NewRequest("POST", "/", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")

		server.addDomain(c)

		metadataPath := filepath.Join(tmpDir, "multi-app-add", "service.yml")
		data, err := os.ReadFile(metadataPath)
		if err != nil {
			t.Fatalf("Failed to read metadata: %v", err)
		}
		var metadata models.ServiceMetadata
		if err := yaml.Unmarshal(data, &metadata); err != nil {
			t.Fatalf("Failed to unmarshal metadata: %v", err)
		}

		if len(metadata.Domains) != 1 {
			t.Fatalf("Expected 1 domain, got %d", len(metadata.Domains))
		}
		if metadata.Domains[0].Service != "app" {
			t.Errorf("Expected service 'app', got %q", metadata.Domains[0].Service)
		}
	})
}

func TestListDeploymentServices(t *testing.T) {
	server, tmpDir, cleanup := setupServiceResolutionServer(t)
	defer cleanup()

	t.Run("returns services for deployment", func(t *testing.T) {
		createDeploymentWithServices(t, tmpDir, "list-svcs", `name: list-svcs
services:
  web:
    image: nginx:latest
  api:
    image: myapi:latest
  worker:
    image: myworker:latest
`, nil)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "name", Value: "list-svcs"}}

		server.listDeploymentServices(c)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", w.Code)
		}

		var response map[string][]models.Service
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("Failed to unmarshal response: %v", err)
		}

		services := response["services"]
		if len(services) != 3 {
			t.Errorf("Expected 3 services, got %d", len(services))
		}
	})

	t.Run("returns 404 for nonexistent deployment", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "name", Value: "does-not-exist"}}

		server.listDeploymentServices(c)

		if w.Code != http.StatusNotFound {
			t.Errorf("Expected status 404, got %d", w.Code)
		}
	})
}

func svcContains(s, substr string) bool {
	return strings.Contains(s, substr)
}
