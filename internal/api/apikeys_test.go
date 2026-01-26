package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
)

func setupAPIKeyTestServer(t *testing.T) (*Server, *gin.Engine, func()) {
	gin.SetMode(gin.TestMode)

	tmpDir, err := os.MkdirTemp("", "apikey_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	cfg := &config.Config{
		DeploymentsPath: tmpDir,
		Auth: config.AuthConfig{
			Enabled:   true,
			JWTSecret: "test-jwt-secret-key-for-testing",
			APIKeys:   []string{"legacy-test-key"},
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
		apikeys := protected.Group("/apikeys")
		apikeys.Use(authMiddleware.RequirePermission(auth.PermAPIKeysRead))
		{
			apikeys.GET("", server.listAPIKeys)
			apikeys.GET("/:id", server.getAPIKey)
		}

		apikeysWrite := protected.Group("/apikeys")
		apikeysWrite.Use(authMiddleware.RequirePermission(auth.PermAPIKeysWrite))
		{
			apikeysWrite.POST("", server.createAPIKey)
		}

		apikeysDelete := protected.Group("/apikeys")
		apikeysDelete.Use(authMiddleware.RequirePermission(auth.PermAPIKeysDelete))
		{
			apikeysDelete.DELETE("/:id", server.deleteAPIKey)
			apikeysDelete.POST("/:id/revoke", server.revokeAPIKey)
		}
	}

	cleanup := func() {
		authManager.Close()
		os.RemoveAll(tmpDir)
		os.Unsetenv("FLATRUN_ADMIN_PASSWORD")
	}

	return server, router, cleanup
}

func apiKeyLogin(t *testing.T, router *gin.Engine, username, password string) string {
	body := map[string]string{
		"username": username,
		"password": password,
	}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Login failed: %d - %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	return resp["token"].(string)
}

func TestCreateAPIKey(t *testing.T) {
	_, router, cleanup := setupAPIKeyTestServer(t)
	defer cleanup()

	token := apiKeyLogin(t, router, "admin", "testadminpass")

	body := map[string]interface{}{
		"name":        "Test API Key",
		"description": "For testing purposes",
	}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/apikeys", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("Expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	apiKey, ok := resp["api_key"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected api_key object in response")
	}

	if apiKey["name"] != "Test API Key" {
		t.Errorf("Expected name 'Test API Key', got %v", apiKey["name"])
	}

	apiKeyResp := resp["api_key"].(map[string]interface{})
	plainKey, ok := apiKeyResp["key"].(string)
	if !ok || plainKey == "" {
		t.Error("Expected key to be returned on creation")
	}

	if len(plainKey) < 10 {
		t.Error("Plain key should be a significant length")
	}
}

func TestCreateAPIKeyWithExpiration(t *testing.T) {
	_, router, cleanup := setupAPIKeyTestServer(t)
	defer cleanup()

	token := apiKeyLogin(t, router, "admin", "testadminpass")

	expiresAt := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	body := map[string]interface{}{
		"name":       "Expiring Key",
		"expires_at": expiresAt,
	}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/apikeys", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("Expected status 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateAPIKeyWithRoleOverride(t *testing.T) {
	_, router, cleanup := setupAPIKeyTestServer(t)
	defer cleanup()

	token := apiKeyLogin(t, router, "admin", "testadminpass")

	body := map[string]interface{}{
		"name": "Viewer Key",
		"role": "viewer",
	}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/apikeys", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("Expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	apiKey := resp["api_key"].(map[string]interface{})
	if apiKey["role"] != "viewer" {
		t.Errorf("Expected role 'viewer', got %v", apiKey["role"])
	}
}

func TestListAPIKeys(t *testing.T) {
	server, router, cleanup := setupAPIKeyTestServer(t)
	defer cleanup()

	admin, _ := server.authManager.GetUserByUsername("admin")
	_, _, _ = server.authManager.CreateAPIKey(admin.ID, "Key 1", "", "", nil, nil, time.Time{})
	_, _, _ = server.authManager.CreateAPIKey(admin.ID, "Key 2", "", "", nil, nil, time.Time{})

	token := apiKeyLogin(t, router, "admin", "testadminpass")

	req := httptest.NewRequest(http.MethodGet, "/api/apikeys", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	keys, ok := resp["api_keys"].([]interface{})
	if !ok {
		t.Fatal("Expected api_keys array in response")
	}

	if len(keys) < 2 {
		t.Errorf("Expected at least 2 keys, got %d", len(keys))
	}
}

func TestGetAPIKey(t *testing.T) {
	server, router, cleanup := setupAPIKeyTestServer(t)
	defer cleanup()

	admin, _ := server.authManager.GetUserByUsername("admin")
	key, _, _ := server.authManager.CreateAPIKey(admin.ID, "Specific Key", "Description", "", nil, nil, time.Time{})

	token := apiKeyLogin(t, router, "admin", "testadminpass")

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/apikeys/%d", key.ID), nil)
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	apiKey := resp["api_key"].(map[string]interface{})
	if apiKey["name"] != "Specific Key" {
		t.Errorf("Expected name 'Specific Key', got %v", apiKey["name"])
	}
}

func TestDeleteAPIKey(t *testing.T) {
	server, router, cleanup := setupAPIKeyTestServer(t)
	defer cleanup()

	admin, _ := server.authManager.GetUserByUsername("admin")
	key, _, _ := server.authManager.CreateAPIKey(admin.ID, "To Delete", "", "", nil, nil, time.Time{})

	token := apiKeyLogin(t, router, "admin", "testadminpass")

	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/apikeys/%d", key.ID), nil)
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	_, err := server.authManager.GetAPIKey(key.ID)
	if err != auth.ErrAPIKeyNotFound {
		t.Error("API key should be deleted")
	}
}

func TestRevokeAPIKey(t *testing.T) {
	server, router, cleanup := setupAPIKeyTestServer(t)
	defer cleanup()

	admin, _ := server.authManager.GetUserByUsername("admin")
	key, plainKey, _ := server.authManager.CreateAPIKey(admin.ID, "To Revoke", "", "", nil, nil, time.Time{})

	_, _, err := server.authManager.ValidateAPIKey(plainKey)
	if err != nil {
		t.Fatalf("Key should be valid before revoke: %v", err)
	}

	token := apiKeyLogin(t, router, "admin", "testadminpass")

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/apikeys/%d/revoke", key.ID), nil)
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	_, _, err = server.authManager.ValidateAPIKey(plainKey)
	if err != auth.ErrAPIKeyInactive {
		t.Errorf("Expected ErrAPIKeyInactive after revoke, got: %v", err)
	}
}

func TestOperatorCanAccessOwnAPIKeys(t *testing.T) {
	server, router, cleanup := setupAPIKeyTestServer(t)
	defer cleanup()

	operator, _ := server.authManager.CreateUser("operator", "", "operatorpass", auth.RoleOperator)

	_, _, _ = server.authManager.CreateAPIKey(operator.ID, "Operator's Key", "", "", nil, nil, time.Time{})

	token := apiKeyLogin(t, router, "operator", "operatorpass")

	req := httptest.NewRequest(http.MethodGet, "/api/apikeys", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	keys := resp["api_keys"].([]interface{})
	if len(keys) != 1 {
		t.Errorf("Operator should see their own 1 key, got %d", len(keys))
	}
}

func TestViewerCannotCreateAPIKey(t *testing.T) {
	server, router, cleanup := setupAPIKeyTestServer(t)
	defer cleanup()

	_, _ = server.authManager.CreateUser("viewer", "", "viewerpass", auth.RoleViewer)

	token := apiKeyLogin(t, router, "viewer", "viewerpass")

	body := map[string]string{"name": "Viewer Key"}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/apikeys", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("Expected status 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateAPIKeyWithDeploymentScope(t *testing.T) {
	_, router, cleanup := setupAPIKeyTestServer(t)
	defer cleanup()

	token := apiKeyLogin(t, router, "admin", "testadminpass")

	body := map[string]interface{}{
		"name":        "Scoped Key",
		"deployments": []string{"app-a", "app-b"},
	}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/apikeys", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("Expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	apiKey := resp["api_key"].(map[string]interface{})
	deployments := apiKey["deployments"].([]interface{})
	if len(deployments) != 2 {
		t.Errorf("Expected 2 deployments, got %d", len(deployments))
	}
}
