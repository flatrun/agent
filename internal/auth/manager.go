package auth

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/flatrun/agent/pkg/config"
)

var (
	ErrUserNotFound     = errors.New("user not found")
	ErrUserExists       = errors.New("user already exists")
	ErrInvalidPassword  = errors.New("invalid password")
	ErrAPIKeyNotFound   = errors.New("api key not found")
	ErrAPIKeyExpired    = errors.New("api key has expired")
	ErrAPIKeyInactive   = errors.New("api key is inactive")
	ErrSessionNotFound  = errors.New("session not found")
	ErrSessionExpired   = errors.New("session has expired")
	ErrSessionRevoked   = errors.New("session has been revoked")
	ErrInvalidRole      = errors.New("invalid role")
	ErrUserInactive     = errors.New("user account is inactive")
	ErrCannotDeleteSelf = errors.New("cannot delete your own account")
)

type Manager struct {
	db     *DB
	config *config.AuthConfig
}

func NewManager(deploymentsPath string, cfg *config.AuthConfig) (*Manager, error) {
	db, err := NewAuthDB(deploymentsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize auth database: %w", err)
	}

	m := &Manager{
		db:     db,
		config: cfg,
	}

	if err := m.ensureAdminUser(); err != nil {
		log.Printf("Warning: failed to ensure admin user: %v", err)
	}

	return m, nil
}

func (m *Manager) Close() error {
	return m.db.Close()
}

func (m *Manager) ensureAdminUser() error {
	count, err := m.db.CountUsers()
	if err != nil {
		return err
	}

	if count > 0 {
		return nil
	}

	adminPassword := os.Getenv("FLATRUN_ADMIN_PASSWORD")
	if adminPassword == "" {
		adminPassword = "admin"
		log.Println("WARNING: No users exist and FLATRUN_ADMIN_PASSWORD not set. Creating admin user with default password 'admin'. Please change this immediately!")
	}

	_, err = m.CreateUser("admin", "", adminPassword, RoleAdmin)
	if err != nil {
		return fmt.Errorf("failed to create admin user: %w", err)
	}

	log.Println("Created initial admin user")
	return nil
}

func (m *Manager) CreateUser(username, email, password string, role Role) (*User, error) {
	if !role.IsValid() {
		return nil, ErrInvalidRole
	}

	passwordHash, err := HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	uid, err := GenerateUID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate UID: %w", err)
	}

	now := time.Now()
	user := &User{
		UID:          uid,
		Username:     username,
		Email:        email,
		PasswordHash: passwordHash,
		Role:         role,
		IsActive:     true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	id, err := m.db.CreateUser(user)
	if err != nil {
		return nil, err
	}

	user.ID = id
	return user, nil
}

func (m *Manager) GetUser(id int64) (*User, error) {
	user, err := m.db.GetUserByID(id)
	if err == sql.ErrNoRows {
		return nil, ErrUserNotFound
	}
	return user, err
}

func (m *Manager) GetUserByUID(uid string) (*User, error) {
	user, err := m.db.GetUserByUID(uid)
	if err == sql.ErrNoRows {
		return nil, ErrUserNotFound
	}
	return user, err
}

func (m *Manager) GetUserByUsername(username string) (*User, error) {
	user, err := m.db.GetUserByUsername(username)
	if err == sql.ErrNoRows {
		return nil, ErrUserNotFound
	}
	return user, err
}

func (m *Manager) GetUsers() ([]User, error) {
	return m.db.GetUsers()
}

func (m *Manager) UpdateUser(user *User) error {
	if !user.Role.IsValid() {
		return ErrInvalidRole
	}
	return m.db.UpdateUser(user)
}

func (m *Manager) UpdatePassword(userID int64, newPassword string) error {
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	return m.db.UpdateUserPassword(userID, hash)
}

func (m *Manager) DeleteUser(id int64, actorID int64) error {
	if id == actorID {
		return ErrCannotDeleteSelf
	}
	return m.db.DeleteUser(id)
}

func (m *Manager) ValidateCredentials(username, password string) (*User, error) {
	user, err := m.db.GetUserByUsername(username)
	if err == sql.ErrNoRows {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}

	if !user.IsActive {
		return nil, ErrUserInactive
	}

	if !VerifyPassword(password, user.PasswordHash) {
		return nil, ErrInvalidPassword
	}

	_ = m.db.UpdateUserLastLogin(user.ID)
	return user, nil
}

func (m *Manager) CreateAPIKey(userID int64, name, description string, role Role, permissions, deployments []string, expiresAt time.Time) (*APIKey, string, error) {
	plainKey, keyHash, keyID, prefix, err := GenerateAPIKey()
	if err != nil {
		return nil, "", err
	}

	key := &APIKey{
		KeyID:       keyID,
		UserID:      userID,
		Name:        name,
		Description: description,
		KeyHash:     keyHash,
		KeyPrefix:   prefix,
		Role:        role,
		Permissions: permissions,
		Deployments: deployments,
		ExpiresAt:   expiresAt,
		IsActive:    true,
		CreatedAt:   time.Now(),
	}

	id, err := m.db.CreateAPIKey(key)
	if err != nil {
		return nil, "", err
	}

	key.ID = id
	return key, plainKey, nil
}

func (m *Manager) GetAPIKey(id int64) (*APIKey, error) {
	key, err := m.db.GetAPIKeyByID(id)
	if err == sql.ErrNoRows {
		return nil, ErrAPIKeyNotFound
	}
	return key, err
}

func (m *Manager) GetAPIKeysByUser(userID int64) ([]APIKey, error) {
	return m.db.GetAPIKeysByUserID(userID)
}

func (m *Manager) GetAllAPIKeys() ([]APIKey, error) {
	return m.db.GetAllAPIKeys()
}

func (m *Manager) ValidateAPIKey(plainKey string) (*APIKey, *User, error) {
	hash := HashAPIKey(plainKey)
	key, err := m.db.GetAPIKeyByHash(hash)
	if err == sql.ErrNoRows {
		return nil, nil, ErrAPIKeyNotFound
	}
	if err != nil {
		return nil, nil, err
	}

	if !key.IsActive {
		return nil, nil, ErrAPIKeyInactive
	}

	if !key.ExpiresAt.IsZero() && key.ExpiresAt.Before(time.Now()) {
		return nil, nil, ErrAPIKeyExpired
	}

	user, err := m.db.GetUserByID(key.UserID)
	if err != nil {
		return nil, nil, err
	}

	if !user.IsActive {
		return nil, nil, ErrUserInactive
	}

	return key, user, nil
}

func (m *Manager) UpdateAPIKeyLastUsed(keyID int64, ip string) error {
	return m.db.UpdateAPIKeyLastUsed(keyID, ip)
}

func (m *Manager) DeleteAPIKey(id int64) error {
	return m.db.DeleteAPIKey(id)
}

func (m *Manager) DeactivateAPIKey(id int64) error {
	return m.db.DeactivateAPIKey(id)
}

func (m *Manager) CreateSession(userID int64, apiKeyID int64, tokenHash, clientIP string, expiresAt time.Time) (*Session, error) {
	sessionID, err := GenerateSessionID()
	if err != nil {
		return nil, err
	}

	session := &Session{
		SessionID: sessionID,
		UserID:    userID,
		APIKeyID:  apiKeyID,
		TokenHash: tokenHash,
		ExpiresAt: expiresAt,
		CreatedAt: time.Now(),
		ClientIP:  clientIP,
	}

	id, err := m.db.CreateSession(session)
	if err != nil {
		return nil, err
	}

	session.ID = id
	return session, nil
}

func (m *Manager) GetSessionByToken(tokenHash string) (*Session, error) {
	session, err := m.db.GetSessionByTokenHash(tokenHash)
	if err == sql.ErrNoRows {
		return nil, ErrSessionNotFound
	}
	return session, err
}

func (m *Manager) RevokeSession(sessionID string) error {
	return m.db.RevokeSession(sessionID)
}

func (m *Manager) RevokeUserSessions(userID int64) error {
	return m.db.RevokeUserSessions(userID)
}

func (m *Manager) CleanupExpiredSessions() (int64, error) {
	return m.db.CleanupExpiredSessions()
}

func (m *Manager) AssignDeployment(userID int64, deploymentName, accessLevel string, grantedBy int64) error {
	ud := &UserDeployment{
		UserID:         userID,
		DeploymentName: deploymentName,
		AccessLevel:    accessLevel,
		GrantedBy:      grantedBy,
		CreatedAt:      time.Now(),
	}
	_, err := m.db.CreateUserDeployment(ud)
	return err
}

func (m *Manager) GetUserDeployments(userID int64) ([]UserDeployment, error) {
	return m.db.GetUserDeployments(userID)
}

func (m *Manager) GetUserDeploymentsMap(userID int64) (map[string]string, error) {
	return m.db.GetUserDeploymentsMap(userID)
}

func (m *Manager) GetDeploymentUsers(deploymentName string) ([]UserDeployment, error) {
	return m.db.GetDeploymentUsers(deploymentName)
}

func (m *Manager) UpdateUserDeployment(userID int64, deploymentName, accessLevel string) error {
	return m.db.UpdateUserDeployment(userID, deploymentName, accessLevel)
}

func (m *Manager) RemoveDeploymentAccess(userID int64, deploymentName string) error {
	return m.db.DeleteUserDeployment(userID, deploymentName)
}

func (m *Manager) BuildActorContext(user *User, apiKey *APIKey) (*ActorContext, error) {
	actor := &ActorContext{
		User:   user,
		APIKey: apiKey,
	}

	if user != nil {
		actor.UserID = user.ID
		actor.Role = user.Role

		deployments, err := m.GetUserDeploymentsMap(user.ID)
		if err != nil {
			return nil, err
		}
		actor.Deployments = deployments
	}

	if apiKey != nil {
		actor.Type = "api_key"

		if apiKey.Role != "" {
			actor.Role = apiKey.Role
		}

		if len(apiKey.Permissions) > 0 {
			actor.Permissions = apiKey.Permissions
		}
	} else if user != nil {
		actor.Type = "user"
	}

	return actor, nil
}

func (m *Manager) ValidateLegacyAPIKey(key string) bool {
	for _, validKey := range m.config.APIKeys {
		if key == validKey {
			return true
		}
	}
	return false
}

func (m *Manager) GetLegacyKeyIndex(key string) int {
	for i, validKey := range m.config.APIKeys {
		if key == validKey {
			return i
		}
	}
	return -1
}
