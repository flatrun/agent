package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
)

func setupTestServer(t *testing.T) (*Server, *gin.Engine, func()) {
	gin.SetMode(gin.TestMode)

	tmpDir, err := os.MkdirTemp("", "api_test")
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

	authManager, err := auth.NewManager(tmpDir, &cfg.Auth, true)
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
		protected.GET("/users/me", server.getCurrentUser)
		protected.PUT("/users/me", server.updateCurrentUser)
		protected.PUT("/users/me/password", server.updateCurrentUserPassword)

		admin := protected.Group("")
		admin.Use(authMiddleware.RequirePermission(auth.PermUsersRead))
		{
			admin.GET("/users", server.listUsers)
			admin.GET("/users/:id", server.getUser)
		}

		adminWrite := protected.Group("")
		adminWrite.Use(authMiddleware.RequirePermission(auth.PermUsersWrite))
		{
			adminWrite.POST("/users", server.createUser)
			adminWrite.PUT("/users/:id", server.updateUser)
		}

		adminDelete := protected.Group("")
		adminDelete.Use(authMiddleware.RequirePermission(auth.PermUsersDelete))
		{
			adminDelete.DELETE("/users/:id", server.deleteUser)
		}
	}

	cleanup := func() {
		authManager.Close()
		os.RemoveAll(tmpDir)
		os.Unsetenv("FLATRUN_ADMIN_PASSWORD")
	}

	return server, router, cleanup
}

func loginAndGetToken(t *testing.T, router *gin.Engine, username, password string) string {
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

	token, ok := resp["token"].(string)
	if !ok {
		t.Fatal("Token not returned in login response")
	}

	return token
}

func TestListUsersAsAdmin(t *testing.T) {
	_, router, cleanup := setupTestServer(t)
	defer cleanup()

	token := loginAndGetToken(t, router, "admin", "testadminpass")

	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	users, ok := resp["users"].([]interface{})
	if !ok {
		t.Fatal("Expected users array in response")
	}

	if len(users) < 1 {
		t.Error("Expected at least 1 user (admin)")
	}
}

func TestCreateUserAsAdmin(t *testing.T) {
	_, router, cleanup := setupTestServer(t)
	defer cleanup()

	token := loginAndGetToken(t, router, "admin", "testadminpass")

	body := map[string]string{
		"username": "newuser",
		"email":    "new@example.com",
		"password": "newpassword123",
		"role":     "operator",
	}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("Expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	user, ok := resp["user"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected user object in response")
	}

	if user["username"] != "newuser" {
		t.Errorf("Expected username 'newuser', got %v", user["username"])
	}

	if user["role"] != "operator" {
		t.Errorf("Expected role 'operator', got %v", user["role"])
	}
}

func TestCreateUserInvalidRole(t *testing.T) {
	_, router, cleanup := setupTestServer(t)
	defer cleanup()

	token := loginAndGetToken(t, router, "admin", "testadminpass")

	body := map[string]string{
		"username": "baduser",
		"password": "password",
		"role":     "superadmin",
	}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetCurrentUser(t *testing.T) {
	_, router, cleanup := setupTestServer(t)
	defer cleanup()

	token := loginAndGetToken(t, router, "admin", "testadminpass")

	req := httptest.NewRequest(http.MethodGet, "/api/users/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	user, ok := resp["user"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected user object in response")
	}

	if user["username"] != "admin" {
		t.Errorf("Expected username 'admin', got %v", user["username"])
	}

	if user["role"] != "admin" {
		t.Errorf("Expected role 'admin', got %v", user["role"])
	}

	perms, ok := resp["permissions"].([]interface{})
	if !ok {
		t.Fatal("Expected permissions array in response")
	}

	if len(perms) == 0 {
		t.Error("Admin should have permissions")
	}
}

func TestUpdateCurrentUserPassword(t *testing.T) {
	server, router, cleanup := setupTestServer(t)
	defer cleanup()

	_, _ = server.authManager.CreateUser("passuser", "", "oldpassword", auth.RoleViewer, nil)

	token := loginAndGetToken(t, router, "passuser", "oldpassword")

	body := map[string]string{
		"current_password": "oldpassword",
		"new_password":     "newpassword123",
	}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPut, "/api/users/me/password", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	newToken := loginAndGetToken(t, router, "passuser", "newpassword123")
	if newToken == "" {
		t.Error("Should be able to login with new password")
	}
}

func TestUpdateCurrentUserPasswordWrongCurrent(t *testing.T) {
	server, router, cleanup := setupTestServer(t)
	defer cleanup()

	_, _ = server.authManager.CreateUser("wrongpass", "", "correctpassword", auth.RoleViewer, nil)

	token := loginAndGetToken(t, router, "wrongpass", "correctpassword")

	body := map[string]string{
		"current_password": "wrongpassword",
		"new_password":     "newpassword",
	}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPut, "/api/users/me/password", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestViewerCannotAccessUsersList(t *testing.T) {
	server, router, cleanup := setupTestServer(t)
	defer cleanup()

	_, _ = server.authManager.CreateUser("viewer", "", "viewerpass", auth.RoleViewer, nil)

	token := loginAndGetToken(t, router, "viewer", "viewerpass")

	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("Expected status 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestOperatorCannotAccessUsersList(t *testing.T) {
	server, router, cleanup := setupTestServer(t)
	defer cleanup()

	_, _ = server.authManager.CreateUser("operator", "", "operatorpass", auth.RoleOperator, nil)

	token := loginAndGetToken(t, router, "operator", "operatorpass")

	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("Expected status 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUpdateUser(t *testing.T) {
	server, router, cleanup := setupTestServer(t)
	defer cleanup()

	user, _ := server.authManager.CreateUser("updateme", "", "password", auth.RoleViewer, nil)

	token := loginAndGetToken(t, router, "admin", "testadminpass")

	body := map[string]interface{}{
		"role":      "operator",
		"email":     "updated@example.com",
		"is_active": false,
	}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPut, "/api/users/"+itoa(user.ID), bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	respUser := resp["user"].(map[string]interface{})
	if respUser["role"] != "operator" {
		t.Errorf("Expected role 'operator', got %v", respUser["role"])
	}
	if respUser["email"] != "updated@example.com" {
		t.Errorf("Expected email 'updated@example.com', got %v", respUser["email"])
	}
	if respUser["is_active"] != false {
		t.Errorf("Expected is_active false, got %v", respUser["is_active"])
	}
}

func TestDeleteUser(t *testing.T) {
	server, router, cleanup := setupTestServer(t)
	defer cleanup()

	user, _ := server.authManager.CreateUser("deleteme", "", "password", auth.RoleViewer, nil)

	token := loginAndGetToken(t, router, "admin", "testadminpass")

	req := httptest.NewRequest(http.MethodDelete, "/api/users/"+itoa(user.ID), nil)
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	_, err := server.authManager.GetUser(user.ID)
	if err != auth.ErrUserNotFound {
		t.Error("User should be deleted")
	}
}

func TestDeleteUserCannotDeleteSelf(t *testing.T) {
	_, router, cleanup := setupTestServer(t)
	defer cleanup()

	token := loginAndGetToken(t, router, "admin", "testadminpass")

	req := httptest.NewRequest(http.MethodDelete, "/api/users/1", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUnauthorizedAccess(t *testing.T) {
	_, router, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestInvalidToken(t *testing.T) {
	_, router, cleanup := setupTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/users/me", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d: %s", w.Code, w.Body.String())
	}
}

func itoa(n int64) string {
	return fmt.Sprintf("%d", n)
}
