package auth

import (
	"os"
	"testing"
	"time"

	"github.com/flatrun/agent/pkg/config"
)

func setupTestManager(t *testing.T) (*Manager, func()) {
	tmpDir, err := os.MkdirTemp("", "manager_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	cfg := &config.AuthConfig{
		JWTSecret: "test-secret",
		APIKeys:   []string{"legacy-key-1", "legacy-key-2"},
	}

	os.Setenv("FLATRUN_ADMIN_PASSWORD", "testadminpass")
	defer os.Unsetenv("FLATRUN_ADMIN_PASSWORD")

	manager, err := NewManager(tmpDir, cfg)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create manager: %v", err)
	}

	cleanup := func() {
		manager.Close()
		os.RemoveAll(tmpDir)
	}

	return manager, cleanup
}

func TestNewManager(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	if manager == nil {
		t.Fatal("NewManager returned nil")
	}
}

func TestManagerCreatesAdminUser(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "manager_admin_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	os.Setenv("FLATRUN_ADMIN_PASSWORD", "secureadminpass")
	defer os.Unsetenv("FLATRUN_ADMIN_PASSWORD")

	cfg := &config.AuthConfig{JWTSecret: "test"}
	manager, err := NewManager(tmpDir, cfg)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer manager.Close()

	admin, err := manager.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("Admin user should exist: %v", err)
	}

	if admin.Role != RoleAdmin {
		t.Errorf("Admin user role = %s, want admin", admin.Role)
	}

	if !admin.IsActive {
		t.Error("Admin user should be active")
	}
}

func TestManagerCreateUser(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	user, err := manager.CreateUser("testuser", "test@example.com", "password123", RoleOperator, nil)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	if user.Username != "testuser" {
		t.Errorf("Username = %s, want testuser", user.Username)
	}

	if user.Role != RoleOperator {
		t.Errorf("Role = %s, want operator", user.Role)
	}

	if user.UID == "" {
		t.Error("UID should be generated")
	}
}

func TestManagerCreateUserInvalidRole(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	_, err := manager.CreateUser("baduser", "", "pass", Role("invalid"), nil)
	if err != ErrInvalidRole {
		t.Errorf("Expected ErrInvalidRole, got %v", err)
	}
}

func TestManagerGetUser(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	created, _ := manager.CreateUser("findme", "", "pass", RoleViewer, nil)

	found, err := manager.GetUser(created.ID)
	if err != nil {
		t.Fatalf("GetUser failed: %v", err)
	}

	if found.Username != "findme" {
		t.Errorf("Username = %s, want findme", found.Username)
	}
}

func TestManagerGetUserNotFound(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	_, err := manager.GetUser(99999)
	if err != ErrUserNotFound {
		t.Errorf("Expected ErrUserNotFound, got %v", err)
	}
}

func TestManagerValidateCredentials(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	_, _ = manager.CreateUser("authuser", "", "correctpassword", RoleOperator, nil)

	user, err := manager.ValidateCredentials("authuser", "correctpassword")
	if err != nil {
		t.Fatalf("ValidateCredentials failed: %v", err)
	}

	if user.Username != "authuser" {
		t.Errorf("Username = %s, want authuser", user.Username)
	}
}

func TestManagerValidateCredentialsWrongPassword(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	_, _ = manager.CreateUser("authuser", "", "correctpassword", RoleOperator, nil)

	_, err := manager.ValidateCredentials("authuser", "wrongpassword")
	if err != ErrInvalidPassword {
		t.Errorf("Expected ErrInvalidPassword, got %v", err)
	}
}

func TestManagerValidateCredentialsUserNotFound(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	_, err := manager.ValidateCredentials("nonexistent", "password")
	if err != ErrUserNotFound {
		t.Errorf("Expected ErrUserNotFound, got %v", err)
	}
}

func TestManagerValidateCredentialsInactiveUser(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	user, _ := manager.CreateUser("inactive", "", "pass", RoleViewer, nil)
	user.IsActive = false
	_ = manager.UpdateUser(user)

	_, err := manager.ValidateCredentials("inactive", "pass")
	if err != ErrUserInactive {
		t.Errorf("Expected ErrUserInactive, got %v", err)
	}
}

func TestManagerDeleteUser(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	user, _ := manager.CreateUser("todelete", "", "pass", RoleViewer, nil)
	actorID := int64(99999)

	err := manager.DeleteUser(user.ID, actorID)
	if err != nil {
		t.Fatalf("DeleteUser failed: %v", err)
	}

	_, err = manager.GetUser(user.ID)
	if err != ErrUserNotFound {
		t.Error("User should be deleted")
	}
}

func TestManagerDeleteUserCannotDeleteSelf(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	user, _ := manager.CreateUser("selfdelete", "", "pass", RoleAdmin, nil)

	err := manager.DeleteUser(user.ID, user.ID)
	if err != ErrCannotDeleteSelf {
		t.Errorf("Expected ErrCannotDeleteSelf, got %v", err)
	}
}

func TestManagerUpdatePassword(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	user, _ := manager.CreateUser("passchange", "", "oldpass", RoleViewer, nil)

	err := manager.UpdatePassword(user.ID, "newpassword")
	if err != nil {
		t.Fatalf("UpdatePassword failed: %v", err)
	}

	_, err = manager.ValidateCredentials("passchange", "newpassword")
	if err != nil {
		t.Error("Should be able to login with new password")
	}

	_, err = manager.ValidateCredentials("passchange", "oldpass")
	if err != ErrInvalidPassword {
		t.Error("Old password should not work")
	}
}

func TestManagerCreateAPIKey(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	user, _ := manager.CreateUser("keyowner", "", "pass", RoleOperator, nil)

	key, plainKey, err := manager.CreateAPIKey(user.ID, "Test Key", "Testing", "", nil, nil, time.Time{})
	if err != nil {
		t.Fatalf("CreateAPIKey failed: %v", err)
	}

	if plainKey == "" {
		t.Error("Plain key should be returned")
	}

	if key.Name != "Test Key" {
		t.Errorf("Name = %s, want Test Key", key.Name)
	}

	if key.UserID != user.ID {
		t.Errorf("UserID = %d, want %d", key.UserID, user.ID)
	}
}

func TestManagerValidateAPIKey(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	user, _ := manager.CreateUser("keyuser", "", "pass", RoleOperator, nil)
	_, plainKey, _ := manager.CreateAPIKey(user.ID, "Valid Key", "", "", nil, nil, time.Time{})

	key, foundUser, err := manager.ValidateAPIKey(plainKey)
	if err != nil {
		t.Fatalf("ValidateAPIKey failed: %v", err)
	}

	if key.Name != "Valid Key" {
		t.Errorf("Key name = %s, want Valid Key", key.Name)
	}

	if foundUser.ID != user.ID {
		t.Errorf("User ID = %d, want %d", foundUser.ID, user.ID)
	}
}

func TestManagerValidateAPIKeyExpired(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	user, _ := manager.CreateUser("expiredkey", "", "pass", RoleOperator, nil)
	_, plainKey, _ := manager.CreateAPIKey(user.ID, "Expired Key", "", "", nil, nil, time.Now().Add(-1*time.Hour))

	_, _, err := manager.ValidateAPIKey(plainKey)
	if err != ErrAPIKeyExpired {
		t.Errorf("Expected ErrAPIKeyExpired, got %v", err)
	}
}

func TestManagerValidateAPIKeyInactive(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	user, _ := manager.CreateUser("inactivekey", "", "pass", RoleOperator, nil)
	key, plainKey, _ := manager.CreateAPIKey(user.ID, "Inactive Key", "", "", nil, nil, time.Time{})

	_ = manager.DeactivateAPIKey(key.ID)

	_, _, err := manager.ValidateAPIKey(plainKey)
	if err != ErrAPIKeyInactive {
		t.Errorf("Expected ErrAPIKeyInactive, got %v", err)
	}
}

func TestManagerValidateAPIKeyNotFound(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	_, _, err := manager.ValidateAPIKey("fr_nonexistent_key")
	if err != ErrAPIKeyNotFound {
		t.Errorf("Expected ErrAPIKeyNotFound, got %v", err)
	}
}

func TestManagerSessionLifecycle(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	user, _ := manager.CreateUser("sessionuser", "", "pass", RoleViewer, nil)

	tokenHash := HashAPIKey("test-token")
	session, err := manager.CreateSession(user.ID, 0, "", tokenHash, "127.0.0.1", time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	found, err := manager.GetSessionByToken(tokenHash)
	if err != nil {
		t.Fatalf("GetSessionByToken failed: %v", err)
	}

	if found.SessionID != session.SessionID {
		t.Error("Session ID mismatch")
	}

	err = manager.RevokeSession(session.SessionID)
	if err != nil {
		t.Fatalf("RevokeSession failed: %v", err)
	}

	_, err = manager.GetSessionByToken(tokenHash)
	if err == nil {
		t.Error("Revoked session should not be found")
	}
}

func TestManagerDeploymentAccess(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	user, _ := manager.CreateUser("deployuser", "", "pass", RoleOperator, nil)
	admin, _ := manager.GetUserByUsername("admin")

	err := manager.AssignDeployment(user.ID, "my-app", "write", admin.ID)
	if err != nil {
		t.Fatalf("AssignDeployment failed: %v", err)
	}

	deployments, err := manager.GetUserDeployments(user.ID)
	if err != nil {
		t.Fatalf("GetUserDeployments failed: %v", err)
	}

	if len(deployments) != 1 {
		t.Fatalf("Expected 1 deployment, got %d", len(deployments))
	}

	if deployments[0].DeploymentName != "my-app" {
		t.Errorf("DeploymentName = %s, want my-app", deployments[0].DeploymentName)
	}

	if deployments[0].AccessLevel != "write" {
		t.Errorf("AccessLevel = %s, want write", deployments[0].AccessLevel)
	}
}

func TestManagerDeploymentAccessMap(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	user, _ := manager.CreateUser("mapuser", "", "pass", RoleOperator, nil)
	admin, _ := manager.GetUserByUsername("admin")

	_ = manager.AssignDeployment(user.ID, "app-a", "read", admin.ID)
	_ = manager.AssignDeployment(user.ID, "app-b", "write", admin.ID)

	depMap, err := manager.GetUserDeploymentsMap(user.ID)
	if err != nil {
		t.Fatalf("GetUserDeploymentsMap failed: %v", err)
	}

	if depMap["app-a"] != "read" {
		t.Errorf("app-a access = %s, want read", depMap["app-a"])
	}

	if depMap["app-b"] != "write" {
		t.Errorf("app-b access = %s, want write", depMap["app-b"])
	}
}

func TestManagerUpdateDeploymentAccess(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	user, _ := manager.CreateUser("updateaccess", "", "pass", RoleOperator, nil)
	admin, _ := manager.GetUserByUsername("admin")

	_ = manager.AssignDeployment(user.ID, "my-app", "read", admin.ID)

	err := manager.UpdateUserDeployment(user.ID, "my-app", "admin")
	if err != nil {
		t.Fatalf("UpdateUserDeployment failed: %v", err)
	}

	depMap, _ := manager.GetUserDeploymentsMap(user.ID)
	if depMap["my-app"] != "admin" {
		t.Errorf("Access level = %s, want admin", depMap["my-app"])
	}
}

func TestManagerRemoveDeploymentAccess(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	user, _ := manager.CreateUser("removeaccess", "", "pass", RoleOperator, nil)
	admin, _ := manager.GetUserByUsername("admin")

	_ = manager.AssignDeployment(user.ID, "to-remove", "write", admin.ID)

	err := manager.RemoveDeploymentAccess(user.ID, "to-remove")
	if err != nil {
		t.Fatalf("RemoveDeploymentAccess failed: %v", err)
	}

	deployments, _ := manager.GetUserDeployments(user.ID)
	if len(deployments) != 0 {
		t.Errorf("Expected 0 deployments, got %d", len(deployments))
	}
}

func TestManagerBuildActorContext(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	user, _ := manager.CreateUser("actoruser", "", "pass", RoleOperator, nil)
	admin, _ := manager.GetUserByUsername("admin")
	_ = manager.AssignDeployment(user.ID, "my-app", "write", admin.ID)

	actor, err := manager.BuildActorContext(user, nil)
	if err != nil {
		t.Fatalf("BuildActorContext failed: %v", err)
	}

	if actor.Type != "user" {
		t.Errorf("Type = %s, want user", actor.Type)
	}

	if actor.Role != RoleOperator {
		t.Errorf("Role = %s, want operator", actor.Role)
	}

	if actor.Deployments["my-app"] != "write" {
		t.Error("Deployments should include my-app with write access")
	}
}

func TestManagerBuildActorContextWithAPIKey(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	user, _ := manager.CreateUser("apikeyactor", "", "pass", RoleOperator, nil)
	key, _, _ := manager.CreateAPIKey(user.ID, "Test Key", "", RoleViewer, []string{"deployments:read"}, nil, time.Time{})

	fetchedKey, _ := manager.GetAPIKey(key.ID)

	actor, err := manager.BuildActorContext(user, fetchedKey)
	if err != nil {
		t.Fatalf("BuildActorContext failed: %v", err)
	}

	if actor.Type != "api_key" {
		t.Errorf("Type = %s, want api_key", actor.Type)
	}

	if actor.Role != RoleViewer {
		t.Errorf("API key role override should make Role = viewer, got %s", actor.Role)
	}
}

func TestManagerLegacyAPIKeys(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	if !manager.ValidateLegacyAPIKey("legacy-key-1") {
		t.Error("Should validate legacy-key-1")
	}

	if !manager.ValidateLegacyAPIKey("legacy-key-2") {
		t.Error("Should validate legacy-key-2")
	}

	if manager.ValidateLegacyAPIKey("invalid-key") {
		t.Error("Should not validate invalid key")
	}
}

func TestManagerGetLegacyKeyIndex(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	idx := manager.GetLegacyKeyIndex("legacy-key-1")
	if idx != 0 {
		t.Errorf("Index = %d, want 0", idx)
	}

	idx = manager.GetLegacyKeyIndex("legacy-key-2")
	if idx != 1 {
		t.Errorf("Index = %d, want 1", idx)
	}

	idx = manager.GetLegacyKeyIndex("nonexistent")
	if idx != -1 {
		t.Errorf("Index = %d, want -1", idx)
	}
}

func TestManagerGetDeploymentUsers(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()

	user1, _ := manager.CreateUser("depuser1", "", "pass", RoleOperator, nil)
	user2, _ := manager.CreateUser("depuser2", "", "pass", RoleViewer, nil)
	admin, _ := manager.GetUserByUsername("admin")

	_ = manager.AssignDeployment(user1.ID, "shared-app", "write", admin.ID)
	_ = manager.AssignDeployment(user2.ID, "shared-app", "read", admin.ID)

	users, err := manager.GetDeploymentUsers("shared-app")
	if err != nil {
		t.Fatalf("GetDeploymentUsers failed: %v", err)
	}

	if len(users) != 2 {
		t.Errorf("Expected 2 users, got %d", len(users))
	}
}
