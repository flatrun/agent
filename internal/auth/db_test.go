package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setupTestDB(t *testing.T) (*DB, func()) {
	tmpDir, err := os.MkdirTemp("", "auth_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	db, err := NewAuthDB(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create DB: %v", err)
	}

	cleanup := func() {
		db.Close()
		os.RemoveAll(tmpDir)
	}

	return db, cleanup
}

func TestNewAuthDB(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	if db == nil {
		t.Fatal("NewAuthDB returned nil")
	}

	if db.conn == nil {
		t.Fatal("DB connection is nil")
	}
}

func TestAuthDBPath(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "auth_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	db, err := NewAuthDB(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	expectedPath := filepath.Join(tmpDir, ".flatrun", "auth.db")
	if db.path != expectedPath {
		t.Errorf("DB path = %s, want %s", db.path, expectedPath)
	}

	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Error("Database file was not created")
	}
}

func TestCreateAndGetUser(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	user := &User{
		UID:          "test-uid-123",
		Username:     "testuser",
		Email:        "test@example.com",
		PasswordHash: "hashedpassword",
		Role:         RoleOperator,
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	id, err := db.CreateUser(user)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	if id <= 0 {
		t.Error("CreateUser should return positive ID")
	}

	retrieved, err := db.GetUserByID(id)
	if err != nil {
		t.Fatalf("GetUserByID failed: %v", err)
	}

	if retrieved.Username != user.Username {
		t.Errorf("Username = %s, want %s", retrieved.Username, user.Username)
	}

	if retrieved.Role != user.Role {
		t.Errorf("Role = %s, want %s", retrieved.Role, user.Role)
	}
}

func TestGetUserByUsername(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	user := &User{
		UID:          "test-uid-456",
		Username:     "findme",
		PasswordHash: "hash",
		Role:         RoleViewer,
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	_, _ = db.CreateUser(user)

	found, err := db.GetUserByUsername("findme")
	if err != nil {
		t.Fatalf("GetUserByUsername failed: %v", err)
	}

	if found.Username != "findme" {
		t.Errorf("Username = %s, want findme", found.Username)
	}
}

func TestGetUserByUID(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	user := &User{
		UID:          "unique-uid-789",
		Username:     "uiduser",
		PasswordHash: "hash",
		Role:         RoleAdmin,
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	_, _ = db.CreateUser(user)

	found, err := db.GetUserByUID("unique-uid-789")
	if err != nil {
		t.Fatalf("GetUserByUID failed: %v", err)
	}

	if found.UID != "unique-uid-789" {
		t.Errorf("UID = %s, want unique-uid-789", found.UID)
	}
}

func TestUpdateUser(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	user := &User{
		UID:          "update-uid",
		Username:     "updateme",
		PasswordHash: "hash",
		Role:         RoleViewer,
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	id, _ := db.CreateUser(user)
	user.ID = id
	user.Role = RoleOperator
	user.Email = "updated@example.com"

	err := db.UpdateUser(user)
	if err != nil {
		t.Fatalf("UpdateUser failed: %v", err)
	}

	updated, _ := db.GetUserByID(id)
	if updated.Role != RoleOperator {
		t.Errorf("Role = %s, want operator", updated.Role)
	}
	if updated.Email != "updated@example.com" {
		t.Errorf("Email = %s, want updated@example.com", updated.Email)
	}
}

func TestDeleteUser(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	user := &User{
		UID:          "delete-uid",
		Username:     "deleteme",
		PasswordHash: "hash",
		Role:         RoleViewer,
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	id, _ := db.CreateUser(user)

	err := db.DeleteUser(id)
	if err != nil {
		t.Fatalf("DeleteUser failed: %v", err)
	}

	_, err = db.GetUserByID(id)
	if err == nil {
		t.Error("GetUserByID should fail after deletion")
	}
}

func TestCountUsers(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	count, _ := db.CountUsers()
	if count != 0 {
		t.Errorf("Initial count = %d, want 0", count)
	}

	_, _ = db.CreateUser(&User{
		UID:          "count-uid-1",
		Username:     "user1",
		PasswordHash: "hash",
		Role:         RoleViewer,
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	})

	_, _ = db.CreateUser(&User{
		UID:          "count-uid-2",
		Username:     "user2",
		PasswordHash: "hash",
		Role:         RoleViewer,
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	})

	count, _ = db.CountUsers()
	if count != 2 {
		t.Errorf("Count = %d, want 2", count)
	}
}

func TestCreateAndGetAPIKey(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	user := &User{
		UID:          "apikey-user-uid",
		Username:     "apikeyuser",
		PasswordHash: "hash",
		Role:         RoleOperator,
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	userID, _ := db.CreateUser(user)

	apiKey := &APIKey{
		KeyID:     "key-id-123",
		UserID:    userID,
		Name:      "Test Key",
		KeyHash:   "hashed-key-value",
		KeyPrefix: "fr_test...",
		IsActive:  true,
		CreatedAt: time.Now(),
	}

	id, err := db.CreateAPIKey(apiKey)
	if err != nil {
		t.Fatalf("CreateAPIKey failed: %v", err)
	}

	retrieved, err := db.GetAPIKeyByID(id)
	if err != nil {
		t.Fatalf("GetAPIKeyByID failed: %v", err)
	}

	if retrieved.Name != "Test Key" {
		t.Errorf("Name = %s, want Test Key", retrieved.Name)
	}

	if retrieved.UserID != userID {
		t.Errorf("UserID = %d, want %d", retrieved.UserID, userID)
	}
}

func TestGetAPIKeyByHash(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	user := &User{
		UID:          "hash-user-uid",
		Username:     "hashuser",
		PasswordHash: "hash",
		Role:         RoleOperator,
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	userID, _ := db.CreateUser(user)

	apiKey := &APIKey{
		KeyID:     "hash-key-id",
		UserID:    userID,
		Name:      "Hash Key",
		KeyHash:   "unique-hash-value",
		KeyPrefix: "fr_hash...",
		IsActive:  true,
		CreatedAt: time.Now(),
	}
	_, _ = db.CreateAPIKey(apiKey)

	found, err := db.GetAPIKeyByHash("unique-hash-value")
	if err != nil {
		t.Fatalf("GetAPIKeyByHash failed: %v", err)
	}

	if found.KeyHash != "unique-hash-value" {
		t.Errorf("KeyHash = %s, want unique-hash-value", found.KeyHash)
	}
}

func TestUserDeployments(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	user := &User{
		UID:          "deploy-user-uid",
		Username:     "deployuser",
		PasswordHash: "hash",
		Role:         RoleOperator,
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	userID, _ := db.CreateUser(user)

	ud := &UserDeployment{
		UserID:         userID,
		DeploymentName: "my-app",
		AccessLevel:    "write",
		CreatedAt:      time.Now(),
	}

	_, err := db.CreateUserDeployment(ud)
	if err != nil {
		t.Fatalf("CreateUserDeployment failed: %v", err)
	}

	deployments, err := db.GetUserDeployments(userID)
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

func TestUserDeploymentsMap(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	user := &User{
		UID:          "map-user-uid",
		Username:     "mapuser",
		PasswordHash: "hash",
		Role:         RoleOperator,
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	userID, _ := db.CreateUser(user)

	_, _ = db.CreateUserDeployment(&UserDeployment{
		UserID:         userID,
		DeploymentName: "app-a",
		AccessLevel:    "read",
		CreatedAt:      time.Now(),
	})

	_, _ = db.CreateUserDeployment(&UserDeployment{
		UserID:         userID,
		DeploymentName: "app-b",
		AccessLevel:    "admin",
		CreatedAt:      time.Now(),
	})

	depMap, err := db.GetUserDeploymentsMap(userID)
	if err != nil {
		t.Fatalf("GetUserDeploymentsMap failed: %v", err)
	}

	if depMap["app-a"] != "read" {
		t.Errorf("app-a access = %s, want read", depMap["app-a"])
	}

	if depMap["app-b"] != "admin" {
		t.Errorf("app-b access = %s, want admin", depMap["app-b"])
	}
}

func TestDeleteUserDeployment(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	user := &User{
		UID:          "del-deploy-uid",
		Username:     "deldeployuser",
		PasswordHash: "hash",
		Role:         RoleOperator,
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	userID, _ := db.CreateUser(user)

	_, _ = db.CreateUserDeployment(&UserDeployment{
		UserID:         userID,
		DeploymentName: "to-remove",
		AccessLevel:    "read",
		CreatedAt:      time.Now(),
	})

	err := db.DeleteUserDeployment(userID, "to-remove")
	if err != nil {
		t.Fatalf("DeleteUserDeployment failed: %v", err)
	}

	deployments, _ := db.GetUserDeployments(userID)
	if len(deployments) != 0 {
		t.Errorf("Expected 0 deployments after deletion, got %d", len(deployments))
	}
}

func TestSessionOperations(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	user := &User{
		UID:          "session-user-uid",
		Username:     "sessionuser",
		PasswordHash: "hash",
		Role:         RoleOperator,
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	userID, _ := db.CreateUser(user)

	session := &Session{
		SessionID: "session-123",
		UserID:    userID,
		TokenHash: "token-hash-value",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		CreatedAt: time.Now(),
		ClientIP:  "127.0.0.1",
	}

	_, err := db.CreateSession(session)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	found, err := db.GetSessionByTokenHash("token-hash-value")
	if err != nil {
		t.Fatalf("GetSessionByTokenHash failed: %v", err)
	}

	if found.SessionID != "session-123" {
		t.Errorf("SessionID = %s, want session-123", found.SessionID)
	}

	err = db.RevokeSession("session-123")
	if err != nil {
		t.Fatalf("RevokeSession failed: %v", err)
	}

	_, err = db.GetSessionByTokenHash("token-hash-value")
	if err == nil {
		t.Error("Revoked session should not be returned")
	}
}
