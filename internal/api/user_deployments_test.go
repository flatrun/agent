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

func setupUserDeploymentsTestServer(t *testing.T) (*Server, *gin.Engine, func()) {
	gin.SetMode(gin.TestMode)

	tmpDir, err := os.MkdirTemp("", "user_deployments_test")
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
		usersRead := protected.Group("")
		usersRead.Use(authMiddleware.RequirePermission(auth.PermUsersRead))
		{
			usersRead.GET("/users/:id/deployments", server.getUserDeployments)
			usersRead.GET("/deployments/:name/users", server.getDeploymentUsers)
		}

		usersWrite := protected.Group("")
		usersWrite.Use(authMiddleware.RequirePermission(auth.PermUsersWrite))
		{
			usersWrite.POST("/users/:id/deployments", server.assignUserDeployment)
			usersWrite.PUT("/users/:id/deployments/:name", server.updateUserDeployment)
			usersWrite.DELETE("/users/:id/deployments/:name", server.removeUserDeployment)
		}
	}

	cleanup := func() {
		authManager.Close()
		os.RemoveAll(tmpDir)
		os.Unsetenv("FLATRUN_ADMIN_PASSWORD")
	}

	return server, router, cleanup
}

func depLogin(t *testing.T, router *gin.Engine, username, password string) string {
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

func TestAssignUserDeployment(t *testing.T) {
	server, router, cleanup := setupUserDeploymentsTestServer(t)
	defer cleanup()

	user, _ := server.authManager.CreateUser("deployuser", "", "password", auth.RoleOperator)

	token := depLogin(t, router, "admin", "testadminpass")

	body := map[string]string{
		"deployment_name": "my-app",
		"access_level":    "write",
	}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/users/%d/deployments", user.ID), bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("Expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	deployments, _ := server.authManager.GetUserDeployments(user.ID)
	if len(deployments) != 1 {
		t.Fatalf("Expected 1 deployment, got %d", len(deployments))
	}

	if deployments[0].DeploymentName != "my-app" {
		t.Errorf("Expected deployment 'my-app', got %s", deployments[0].DeploymentName)
	}

	if deployments[0].AccessLevel != "write" {
		t.Errorf("Expected access level 'write', got %s", deployments[0].AccessLevel)
	}
}

func TestGetUserDeployments(t *testing.T) {
	server, router, cleanup := setupUserDeploymentsTestServer(t)
	defer cleanup()

	user, _ := server.authManager.CreateUser("listuser", "", "password", auth.RoleOperator)
	admin, _ := server.authManager.GetUserByUsername("admin")

	_ = server.authManager.AssignDeployment(user.ID, "app-a", "read", admin.ID)
	_ = server.authManager.AssignDeployment(user.ID, "app-b", "write", admin.ID)

	token := depLogin(t, router, "admin", "testadminpass")

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/users/%d/deployments", user.ID), nil)
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	deployments, ok := resp["deployments"].([]interface{})
	if !ok {
		t.Fatal("Expected deployments array in response")
	}

	if len(deployments) != 2 {
		t.Errorf("Expected 2 deployments, got %d", len(deployments))
	}
}

func TestUpdateUserDeployment(t *testing.T) {
	server, router, cleanup := setupUserDeploymentsTestServer(t)
	defer cleanup()

	user, _ := server.authManager.CreateUser("updateuser", "", "password", auth.RoleOperator)
	admin, _ := server.authManager.GetUserByUsername("admin")

	_ = server.authManager.AssignDeployment(user.ID, "my-app", "read", admin.ID)

	token := depLogin(t, router, "admin", "testadminpass")

	body := map[string]string{
		"access_level": "admin",
	}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/users/%d/deployments/my-app", user.ID), bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	depMap, _ := server.authManager.GetUserDeploymentsMap(user.ID)
	if depMap["my-app"] != "admin" {
		t.Errorf("Expected access level 'admin', got %s", depMap["my-app"])
	}
}

func TestRemoveUserDeployment(t *testing.T) {
	server, router, cleanup := setupUserDeploymentsTestServer(t)
	defer cleanup()

	user, _ := server.authManager.CreateUser("removeuser", "", "password", auth.RoleOperator)
	admin, _ := server.authManager.GetUserByUsername("admin")

	_ = server.authManager.AssignDeployment(user.ID, "to-remove", "write", admin.ID)

	token := depLogin(t, router, "admin", "testadminpass")

	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/users/%d/deployments/to-remove", user.ID), nil)
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	deployments, _ := server.authManager.GetUserDeployments(user.ID)
	if len(deployments) != 0 {
		t.Errorf("Expected 0 deployments after removal, got %d", len(deployments))
	}
}

func TestGetDeploymentUsers(t *testing.T) {
	server, router, cleanup := setupUserDeploymentsTestServer(t)
	defer cleanup()

	user1, _ := server.authManager.CreateUser("depuser1", "", "password", auth.RoleOperator)
	user2, _ := server.authManager.CreateUser("depuser2", "", "password", auth.RoleViewer)
	admin, _ := server.authManager.GetUserByUsername("admin")

	_ = server.authManager.AssignDeployment(user1.ID, "shared-app", "write", admin.ID)
	_ = server.authManager.AssignDeployment(user2.ID, "shared-app", "read", admin.ID)

	token := depLogin(t, router, "admin", "testadminpass")

	req := httptest.NewRequest(http.MethodGet, "/api/deployments/shared-app/users", nil)
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

	if len(users) != 2 {
		t.Errorf("Expected 2 users, got %d", len(users))
	}
}

func TestOperatorCannotAssignDeployment(t *testing.T) {
	server, router, cleanup := setupUserDeploymentsTestServer(t)
	defer cleanup()

	_, _ = server.authManager.CreateUser("operator", "", "operatorpass", auth.RoleOperator)

	token := depLogin(t, router, "operator", "operatorpass")

	body := map[string]string{
		"deployment_name": "my-app",
		"access_level":    "read",
	}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/users/1/deployments", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("Expected status 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestViewerCannotViewUserDeployments(t *testing.T) {
	server, router, cleanup := setupUserDeploymentsTestServer(t)
	defer cleanup()

	_, _ = server.authManager.CreateUser("viewer", "", "viewerpass", auth.RoleViewer)

	token := depLogin(t, router, "viewer", "viewerpass")

	req := httptest.NewRequest(http.MethodGet, "/api/users/1/deployments", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("Expected status 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAssignDeploymentInvalidAccessLevel(t *testing.T) {
	server, router, cleanup := setupUserDeploymentsTestServer(t)
	defer cleanup()

	user, _ := server.authManager.CreateUser("badaccess", "", "password", auth.RoleOperator)

	token := depLogin(t, router, "admin", "testadminpass")

	body := map[string]string{
		"deployment_name": "my-app",
		"access_level":    "superadmin",
	}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/users/%d/deployments", user.ID), bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAssignMultipleDeployments(t *testing.T) {
	server, router, cleanup := setupUserDeploymentsTestServer(t)
	defer cleanup()

	user, _ := server.authManager.CreateUser("multiuser", "", "password", auth.RoleOperator)

	token := depLogin(t, router, "admin", "testadminpass")

	assignments := []struct {
		name  string
		level string
	}{
		{"app-a", "read"},
		{"app-b", "write"},
		{"app-c", "admin"},
	}

	for _, a := range assignments {
		body := map[string]string{
			"deployment_name": a.name,
			"access_level":    a.level,
		}
		jsonBody, _ := json.Marshal(body)

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/users/%d/deployments", user.ID), bytes.NewBuffer(jsonBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusCreated {
			t.Errorf("Failed to assign %s: %d - %s", a.name, w.Code, w.Body.String())
		}
	}

	depMap, _ := server.authManager.GetUserDeploymentsMap(user.ID)

	if len(depMap) != 3 {
		t.Errorf("Expected 3 deployments, got %d", len(depMap))
	}

	if depMap["app-a"] != "read" {
		t.Errorf("Expected app-a access 'read', got %s", depMap["app-a"])
	}
	if depMap["app-b"] != "write" {
		t.Errorf("Expected app-b access 'write', got %s", depMap["app-b"])
	}
	if depMap["app-c"] != "admin" {
		t.Errorf("Expected app-c access 'admin', got %s", depMap["app-c"])
	}
}
