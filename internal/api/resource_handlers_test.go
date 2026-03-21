package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
)

func setupResourceTestServer(t *testing.T) (*gin.Engine, string, func()) {
	gin.SetMode(gin.TestMode)

	tmpDir, err := os.MkdirTemp("", "resource_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	cfg := &config.Config{
		DeploymentsPath: tmpDir,
		Auth: config.AuthConfig{
			Enabled:   true,
			JWTSecret: "test-jwt-secret-for-resources",
			APIKeys:   []string{"test-api-key"},
		},
	}

	os.Setenv("FLATRUN_ADMIN_PASSWORD", "testadminpass")

	authManager, err := auth.NewManager(tmpDir, &cfg.Auth, true)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create auth manager: %v", err)
	}

	manager := docker.NewManager(tmpDir)

	server := &Server{
		config:      cfg,
		authManager: authManager,
		manager:     manager,
	}

	router := gin.New()
	authMiddleware := auth.NewMiddlewareWithManager(&cfg.Auth, authManager)

	api := router.Group("/api")
	api.POST("/auth/login", authMiddleware.Login)

	protected := api.Group("")
	protected.Use(authMiddleware.RequireAuth())
	{
		protected.GET("/containers/:id/resources", authMiddleware.RequirePermission(auth.PermContainersRead), server.getContainerResources)
		protected.PUT("/containers/:id/resources", authMiddleware.RequirePermission(auth.PermContainersWrite), server.updateContainerResources)
		protected.GET("/deployments/:name/resources", authMiddleware.RequirePermission(auth.PermDeploymentsRead), server.getDeploymentResources)
	}

	cleanup := func() {
		authManager.Close()
		os.RemoveAll(tmpDir)
		os.Unsetenv("FLATRUN_ADMIN_PASSWORD")
	}

	token := loginAndGetToken(t, router, "admin", "testadminpass")
	return router, token, cleanup
}

func TestGetContainerResourcesRequiresAuth(t *testing.T) {
	router, _, cleanup := setupResourceTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/containers/abc123/resources", nil)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Error("Expected auth error, got 200")
	}
}

func TestUpdateContainerResourcesEmptyBody(t *testing.T) {
	router, token, cleanup := setupResourceTestServer(t)
	defer cleanup()

	body := map[string]interface{}{}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPut, "/api/containers/abc123/resources", bytes.NewBuffer(jsonBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 for empty update, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUpdateContainerResourcesBadJSON(t *testing.T) {
	router, token, cleanup := setupResourceTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPut, "/api/containers/abc123/resources", bytes.NewBufferString("{invalid"))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 for bad JSON, got %d", w.Code)
	}
}

func TestGetDeploymentResourcesNotFound(t *testing.T) {
	router, token, cleanup := setupResourceTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/deployments/nonexistent/resources", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected 404 for nonexistent deployment, got %d", w.Code)
	}
}

func TestResourceUpdateStructSerialization(t *testing.T) {
	mem := int64(256 * 1024 * 1024)
	cpus := 0.5

	update := docker.ResourceUpdate{
		MemoryLimit: &mem,
		CPUs:        &cpus,
	}

	data, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var parsed docker.ResourceUpdate
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if parsed.MemoryLimit == nil || *parsed.MemoryLimit != mem {
		t.Errorf("MemoryLimit = %v, want %d", parsed.MemoryLimit, mem)
	}
	if parsed.CPUs == nil || *parsed.CPUs != cpus {
		t.Errorf("CPUs = %v, want %f", parsed.CPUs, cpus)
	}
	if parsed.MemorySwap != nil {
		t.Error("MemorySwap should be nil")
	}
	if parsed.CPUShares != nil {
		t.Error("CPUShares should be nil")
	}
}
