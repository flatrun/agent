package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/system"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
)

func setupServerInfoTestServer(t *testing.T) (*gin.Engine, string, func()) {
	gin.SetMode(gin.TestMode)

	tmpDir, err := os.MkdirTemp("", "server_info_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	cfg := &config.Config{
		DeploymentsPath: tmpDir,
		Auth: config.AuthConfig{
			Enabled:   true,
			JWTSecret: "test-jwt-secret-for-server-info",
			APIKeys:   []string{"test-api-key"},
		},
	}

	os.Setenv("FLATRUN_ADMIN_PASSWORD", "testadminpass")

	authManager, err := auth.NewManager(tmpDir, &cfg.Auth)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create auth manager: %v", err)
	}

	server := &Server{
		config:      cfg,
		authManager: authManager,
	}

	router := gin.New()
	authMiddleware := auth.NewMiddlewareWithManager(&cfg.Auth, authManager)

	api := router.Group("/api")
	api.POST("/auth/login", authMiddleware.Login)

	protected := api.Group("")
	protected.Use(authMiddleware.RequireAuth())
	{
		protected.GET("/server/info", authMiddleware.RequirePermission(auth.PermSystemRead), server.getServerInfo)
		protected.GET("/server/network-health", authMiddleware.RequirePermission(auth.PermSystemRead), server.getNetworkHealth)
	}

	cleanup := func() {
		authManager.Close()
		os.RemoveAll(tmpDir)
		os.Unsetenv("FLATRUN_ADMIN_PASSWORD")
	}

	token := loginAndGetToken(t, router, "admin", "testadminpass")
	return router, token, cleanup
}

func TestGetServerInfo(t *testing.T) {
	router, token, cleanup := setupServerInfoTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/server/info", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	serverData, ok := resp["server"]
	if !ok {
		t.Fatal("Response missing 'server' key")
	}

	serverMap, ok := serverData.(map[string]interface{})
	if !ok {
		t.Fatal("server should be an object")
	}

	if _, ok := serverMap["hostname"]; !ok {
		t.Error("server should have hostname")
	}
	if _, ok := serverMap["interfaces"]; !ok {
		t.Error("server should have interfaces")
	}
}

func TestGetServerInfoRequiresAuth(t *testing.T) {
	router, _, cleanup := setupServerInfoTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/server/info", nil)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Error("Expected auth error, got 200")
	}
}

func TestGetNetworkHealth(t *testing.T) {
	router, token, cleanup := setupServerInfoTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/server/network-health", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	healthData, ok := resp["network_health"]
	if !ok {
		t.Fatal("Response missing 'network_health' key")
	}

	healthMap, ok := healthData.(map[string]interface{})
	if !ok {
		t.Fatal("network_health should be an object")
	}

	if _, ok := healthMap["dns"]; !ok {
		t.Error("network_health should have dns")
	}
	if _, ok := healthMap["checked_at"]; !ok {
		t.Error("network_health should have checked_at")
	}
}

func TestServerInfoDirectFunction(t *testing.T) {
	info, err := system.GetServerInfo()
	if err != nil {
		t.Fatalf("GetServerInfo failed: %v", err)
	}

	if info.Hostname == "" {
		t.Error("Hostname should not be empty")
	}
}
