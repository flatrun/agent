package auth

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	conn *sql.DB
	path string
	mu   sync.RWMutex
}

func NewAuthDB(deploymentsPath string) (*DB, error) {
	dbDir := filepath.Join(deploymentsPath, ".flatrun")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, err
	}

	dbPath := filepath.Join(dbDir, "auth.db")
	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}

	conn.SetMaxOpenConns(10)
	conn.SetMaxIdleConns(5)
	conn.SetConnMaxLifetime(time.Hour)

	db := &DB{conn: conn, path: dbPath}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, err
	}

	return db, nil
}

func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.conn.Close()
}

func (db *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid TEXT UNIQUE NOT NULL,
		username TEXT UNIQUE NOT NULL,
		email TEXT,
		password_hash TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT 'viewer',
		is_active BOOLEAN DEFAULT TRUE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		last_login_at DATETIME
	);

	CREATE TABLE IF NOT EXISTS api_keys (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		key_id TEXT UNIQUE NOT NULL,
		user_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		description TEXT,
		key_hash TEXT NOT NULL,
		key_prefix TEXT NOT NULL,
		role TEXT,
		permissions TEXT,
		deployments TEXT,
		expires_at DATETIME,
		last_used_at DATETIME,
		last_used_ip TEXT,
		is_active BOOLEAN DEFAULT TRUE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT UNIQUE NOT NULL,
		user_id INTEGER NOT NULL,
		api_key_id INTEGER,
		token_hash TEXT NOT NULL,
		expires_at DATETIME NOT NULL,
		revoked_at DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		client_ip TEXT,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS user_deployments (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		deployment_name TEXT NOT NULL,
		access_level TEXT NOT NULL DEFAULT 'read',
		granted_by INTEGER,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
		UNIQUE(user_id, deployment_name)
	);

	CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
	CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
	CREATE INDEX IF NOT EXISTS idx_users_uid ON users(uid);
	CREATE INDEX IF NOT EXISTS idx_api_keys_user ON api_keys(user_id);
	CREATE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash);
	CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
	CREATE INDEX IF NOT EXISTS idx_sessions_token ON sessions(token_hash);
	CREATE INDEX IF NOT EXISTS idx_user_deployments_user ON user_deployments(user_id);
	CREATE INDEX IF NOT EXISTS idx_user_deployments_deployment ON user_deployments(deployment_name);
	`

	_, err := db.conn.Exec(schema)
	if err != nil {
		return err
	}

	// Add permissions column to users table if missing (ignore error if column exists)
	_, _ = db.conn.Exec(`ALTER TABLE users ADD COLUMN permissions TEXT`)

	return nil
}

func (db *DB) CreateUser(user *User) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	result, err := db.conn.Exec(`
		INSERT INTO users (uid, username, email, password_hash, role, permissions, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		user.UID, user.Username, user.Email, user.PasswordHash, user.Role,
		user.GetPermissionsJSON(), user.IsActive, user.CreatedAt, user.UpdatedAt,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (db *DB) GetUserByID(id int64) (*User, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var u User
	var email, perms sql.NullString
	var lastLogin sql.NullTime

	err := db.conn.QueryRow(`
		SELECT id, uid, username, email, password_hash, role, permissions, is_active, created_at, updated_at, last_login_at
		FROM users WHERE id = ?`, id).Scan(
		&u.ID, &u.UID, &u.Username, &email, &u.PasswordHash, &u.Role, &perms,
		&u.IsActive, &u.CreatedAt, &u.UpdatedAt, &lastLogin,
	)
	if err != nil {
		return nil, err
	}

	u.Email = email.String
	u.Permissions = ParsePermissionsJSON(perms.String)
	if lastLogin.Valid {
		u.LastLoginAt = lastLogin.Time
	}
	return &u, nil
}

func (db *DB) GetUserByUID(uid string) (*User, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var u User
	var email, perms sql.NullString
	var lastLogin sql.NullTime

	err := db.conn.QueryRow(`
		SELECT id, uid, username, email, password_hash, role, permissions, is_active, created_at, updated_at, last_login_at
		FROM users WHERE uid = ?`, uid).Scan(
		&u.ID, &u.UID, &u.Username, &email, &u.PasswordHash, &u.Role, &perms,
		&u.IsActive, &u.CreatedAt, &u.UpdatedAt, &lastLogin,
	)
	if err != nil {
		return nil, err
	}

	u.Email = email.String
	u.Permissions = ParsePermissionsJSON(perms.String)
	if lastLogin.Valid {
		u.LastLoginAt = lastLogin.Time
	}
	return &u, nil
}

func (db *DB) GetUserByUsername(username string) (*User, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var u User
	var email, perms sql.NullString
	var lastLogin sql.NullTime

	err := db.conn.QueryRow(`
		SELECT id, uid, username, email, password_hash, role, permissions, is_active, created_at, updated_at, last_login_at
		FROM users WHERE username = ?`, username).Scan(
		&u.ID, &u.UID, &u.Username, &email, &u.PasswordHash, &u.Role, &perms,
		&u.IsActive, &u.CreatedAt, &u.UpdatedAt, &lastLogin,
	)
	if err != nil {
		return nil, err
	}

	u.Email = email.String
	u.Permissions = ParsePermissionsJSON(perms.String)
	if lastLogin.Valid {
		u.LastLoginAt = lastLogin.Time
	}
	return &u, nil
}

func (db *DB) GetUsers() ([]User, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	rows, err := db.conn.Query(`
		SELECT id, uid, username, email, password_hash, role, permissions, is_active, created_at, updated_at, last_login_at
		FROM users ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		var email, perms sql.NullString
		var lastLogin sql.NullTime

		if err := rows.Scan(
			&u.ID, &u.UID, &u.Username, &email, &u.PasswordHash, &u.Role, &perms,
			&u.IsActive, &u.CreatedAt, &u.UpdatedAt, &lastLogin,
		); err != nil {
			return nil, err
		}

		u.Email = email.String
		u.Permissions = ParsePermissionsJSON(perms.String)
		if lastLogin.Valid {
			u.LastLoginAt = lastLogin.Time
		}
		users = append(users, u)
	}
	return users, nil
}

func (db *DB) UpdateUser(user *User) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`
		UPDATE users SET username = ?, email = ?, role = ?, permissions = ?, is_active = ?, updated_at = ?
		WHERE id = ?`,
		user.Username, user.Email, user.Role, user.GetPermissionsJSON(), user.IsActive, time.Now(), user.ID,
	)
	return err
}

func (db *DB) UpdateUserPassword(id int64, passwordHash string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		passwordHash, time.Now(), id)
	return err
}

func (db *DB) UpdateUserLastLogin(id int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, time.Now(), id)
	return err
}

func (db *DB) DeleteUser(id int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

func (db *DB) CountUsers() (int, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var count int
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
	return count, err
}

func (db *DB) CreateAPIKey(key *APIKey) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	var roleVal sql.NullString
	if key.Role != "" {
		roleVal = sql.NullString{String: string(key.Role), Valid: true}
	}

	var expiresAt sql.NullTime
	if !key.ExpiresAt.IsZero() {
		expiresAt = sql.NullTime{Time: key.ExpiresAt, Valid: true}
	}

	result, err := db.conn.Exec(`
		INSERT INTO api_keys (key_id, user_id, name, description, key_hash, key_prefix, role, permissions, deployments, expires_at, is_active, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key.KeyID, key.UserID, key.Name, key.Description, key.KeyHash, key.KeyPrefix,
		roleVal, key.GetPermissionsJSON(), key.GetDeploymentsJSON(), expiresAt, key.IsActive, key.CreatedAt,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (db *DB) GetAPIKeyByID(id int64) (*APIKey, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var k APIKey
	var desc, role, perms, deps, lastIP sql.NullString
	var expiresAt, lastUsed sql.NullTime

	err := db.conn.QueryRow(`
		SELECT id, key_id, user_id, name, description, key_hash, key_prefix, role, permissions, deployments,
			expires_at, last_used_at, last_used_ip, is_active, created_at
		FROM api_keys WHERE id = ?`, id).Scan(
		&k.ID, &k.KeyID, &k.UserID, &k.Name, &desc, &k.KeyHash, &k.KeyPrefix, &role, &perms, &deps,
		&expiresAt, &lastUsed, &lastIP, &k.IsActive, &k.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	k.Description = desc.String
	k.Role = Role(role.String)
	k.Permissions = ParsePermissionsJSON(perms.String)
	k.Deployments = ParseDeploymentsJSON(deps.String)
	k.LastUsedIP = lastIP.String
	if expiresAt.Valid {
		k.ExpiresAt = expiresAt.Time
	}
	if lastUsed.Valid {
		k.LastUsedAt = lastUsed.Time
	}
	return &k, nil
}

func (db *DB) GetAPIKeyByHash(hash string) (*APIKey, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var k APIKey
	var desc, role, perms, deps, lastIP sql.NullString
	var expiresAt, lastUsed sql.NullTime

	err := db.conn.QueryRow(`
		SELECT id, key_id, user_id, name, description, key_hash, key_prefix, role, permissions, deployments,
			expires_at, last_used_at, last_used_ip, is_active, created_at
		FROM api_keys WHERE key_hash = ?`, hash).Scan(
		&k.ID, &k.KeyID, &k.UserID, &k.Name, &desc, &k.KeyHash, &k.KeyPrefix, &role, &perms, &deps,
		&expiresAt, &lastUsed, &lastIP, &k.IsActive, &k.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	k.Description = desc.String
	k.Role = Role(role.String)
	k.Permissions = ParsePermissionsJSON(perms.String)
	k.Deployments = ParseDeploymentsJSON(deps.String)
	k.LastUsedIP = lastIP.String
	if expiresAt.Valid {
		k.ExpiresAt = expiresAt.Time
	}
	if lastUsed.Valid {
		k.LastUsedAt = lastUsed.Time
	}
	return &k, nil
}

func (db *DB) GetAPIKeysByUserID(userID int64) ([]APIKey, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	rows, err := db.conn.Query(`
		SELECT id, key_id, user_id, name, description, key_hash, key_prefix, role, permissions, deployments,
			expires_at, last_used_at, last_used_ip, is_active, created_at
		FROM api_keys WHERE user_id = ? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []APIKey
	for rows.Next() {
		var k APIKey
		var desc, role, perms, deps, lastIP sql.NullString
		var expiresAt, lastUsed sql.NullTime

		if err := rows.Scan(
			&k.ID, &k.KeyID, &k.UserID, &k.Name, &desc, &k.KeyHash, &k.KeyPrefix, &role, &perms, &deps,
			&expiresAt, &lastUsed, &lastIP, &k.IsActive, &k.CreatedAt,
		); err != nil {
			return nil, err
		}

		k.Description = desc.String
		k.Role = Role(role.String)
		k.Permissions = ParsePermissionsJSON(perms.String)
		k.Deployments = ParseDeploymentsJSON(deps.String)
		k.LastUsedIP = lastIP.String
		if expiresAt.Valid {
			k.ExpiresAt = expiresAt.Time
		}
		if lastUsed.Valid {
			k.LastUsedAt = lastUsed.Time
		}
		keys = append(keys, k)
	}
	return keys, nil
}

func (db *DB) GetAllAPIKeys() ([]APIKey, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	rows, err := db.conn.Query(`
		SELECT id, key_id, user_id, name, description, key_hash, key_prefix, role, permissions, deployments,
			expires_at, last_used_at, last_used_ip, is_active, created_at
		FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []APIKey
	for rows.Next() {
		var k APIKey
		var desc, role, perms, deps, lastIP sql.NullString
		var expiresAt, lastUsed sql.NullTime

		if err := rows.Scan(
			&k.ID, &k.KeyID, &k.UserID, &k.Name, &desc, &k.KeyHash, &k.KeyPrefix, &role, &perms, &deps,
			&expiresAt, &lastUsed, &lastIP, &k.IsActive, &k.CreatedAt,
		); err != nil {
			return nil, err
		}

		k.Description = desc.String
		k.Role = Role(role.String)
		k.Permissions = ParsePermissionsJSON(perms.String)
		k.Deployments = ParseDeploymentsJSON(deps.String)
		k.LastUsedIP = lastIP.String
		if expiresAt.Valid {
			k.ExpiresAt = expiresAt.Time
		}
		if lastUsed.Valid {
			k.LastUsedAt = lastUsed.Time
		}
		keys = append(keys, k)
	}
	return keys, nil
}

func (db *DB) UpdateAPIKeyLastUsed(id int64, ip string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`UPDATE api_keys SET last_used_at = ?, last_used_ip = ? WHERE id = ?`,
		time.Now(), ip, id)
	return err
}

func (db *DB) DeleteAPIKey(id int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`DELETE FROM api_keys WHERE id = ?`, id)
	return err
}

func (db *DB) DeactivateAPIKey(id int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`UPDATE api_keys SET is_active = FALSE WHERE id = ?`, id)
	return err
}

func (db *DB) CreateSession(session *Session) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	var apiKeyID sql.NullInt64
	if session.APIKeyID > 0 {
		apiKeyID = sql.NullInt64{Int64: session.APIKeyID, Valid: true}
	}

	result, err := db.conn.Exec(`
		INSERT INTO sessions (session_id, user_id, api_key_id, token_hash, expires_at, created_at, client_ip)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		session.SessionID, session.UserID, apiKeyID, session.TokenHash,
		session.ExpiresAt, session.CreatedAt, session.ClientIP,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (db *DB) GetSessionByID(sessionID string) (*Session, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var s Session
	var apiKeyID sql.NullInt64
	var revokedAt sql.NullTime
	var clientIP sql.NullString

	err := db.conn.QueryRow(`
		SELECT id, session_id, user_id, api_key_id, token_hash, expires_at, revoked_at, created_at, client_ip
		FROM sessions WHERE session_id = ?`, sessionID).Scan(
		&s.ID, &s.SessionID, &s.UserID, &apiKeyID, &s.TokenHash,
		&s.ExpiresAt, &revokedAt, &s.CreatedAt, &clientIP,
	)
	if err != nil {
		return nil, err
	}

	if apiKeyID.Valid {
		s.APIKeyID = apiKeyID.Int64
	}
	if revokedAt.Valid {
		s.RevokedAt = revokedAt.Time
	}
	s.ClientIP = clientIP.String
	return &s, nil
}

func (db *DB) GetSessionByTokenHash(hash string) (*Session, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var s Session
	var apiKeyID sql.NullInt64
	var revokedAt sql.NullTime
	var clientIP sql.NullString

	err := db.conn.QueryRow(`
		SELECT id, session_id, user_id, api_key_id, token_hash, expires_at, revoked_at, created_at, client_ip
		FROM sessions WHERE token_hash = ? AND revoked_at IS NULL AND expires_at > ?`,
		hash, time.Now()).Scan(
		&s.ID, &s.SessionID, &s.UserID, &apiKeyID, &s.TokenHash,
		&s.ExpiresAt, &revokedAt, &s.CreatedAt, &clientIP,
	)
	if err != nil {
		return nil, err
	}

	if apiKeyID.Valid {
		s.APIKeyID = apiKeyID.Int64
	}
	s.ClientIP = clientIP.String
	return &s, nil
}

func (db *DB) RevokeSession(sessionID string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`UPDATE sessions SET revoked_at = ? WHERE session_id = ?`,
		time.Now(), sessionID)
	return err
}

func (db *DB) RevokeUserSessions(userID int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`UPDATE sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`,
		time.Now(), userID)
	return err
}

func (db *DB) CleanupExpiredSessions() (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	result, err := db.conn.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (db *DB) CreateUserDeployment(ud *UserDeployment) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	var grantedBy sql.NullInt64
	if ud.GrantedBy > 0 {
		grantedBy = sql.NullInt64{Int64: ud.GrantedBy, Valid: true}
	}

	result, err := db.conn.Exec(`
		INSERT INTO user_deployments (user_id, deployment_name, access_level, granted_by, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id, deployment_name) DO UPDATE SET access_level = ?, granted_by = ?`,
		ud.UserID, ud.DeploymentName, ud.AccessLevel, grantedBy, ud.CreatedAt,
		ud.AccessLevel, grantedBy,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (db *DB) GetUserDeployments(userID int64) ([]UserDeployment, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	rows, err := db.conn.Query(`
		SELECT id, user_id, deployment_name, access_level, granted_by, created_at
		FROM user_deployments WHERE user_id = ? ORDER BY deployment_name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var deployments []UserDeployment
	for rows.Next() {
		var ud UserDeployment
		var grantedBy sql.NullInt64

		if err := rows.Scan(&ud.ID, &ud.UserID, &ud.DeploymentName, &ud.AccessLevel, &grantedBy, &ud.CreatedAt); err != nil {
			return nil, err
		}

		if grantedBy.Valid {
			ud.GrantedBy = grantedBy.Int64
		}
		deployments = append(deployments, ud)
	}
	return deployments, nil
}

func (db *DB) GetUserDeploymentsMap(userID int64) (map[string]string, error) {
	deployments, err := db.GetUserDeployments(userID)
	if err != nil {
		return nil, err
	}

	m := make(map[string]string)
	for _, d := range deployments {
		m[d.DeploymentName] = d.AccessLevel
	}
	return m, nil
}

func (db *DB) GetDeploymentUsers(deploymentName string) ([]UserDeployment, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	rows, err := db.conn.Query(`
		SELECT id, user_id, deployment_name, access_level, granted_by, created_at
		FROM user_deployments WHERE deployment_name = ?`, deploymentName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var deployments []UserDeployment
	for rows.Next() {
		var ud UserDeployment
		var grantedBy sql.NullInt64

		if err := rows.Scan(&ud.ID, &ud.UserID, &ud.DeploymentName, &ud.AccessLevel, &grantedBy, &ud.CreatedAt); err != nil {
			return nil, err
		}

		if grantedBy.Valid {
			ud.GrantedBy = grantedBy.Int64
		}
		deployments = append(deployments, ud)
	}
	return deployments, nil
}

func (db *DB) UpdateUserDeployment(userID int64, deploymentName, accessLevel string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`
		UPDATE user_deployments SET access_level = ? WHERE user_id = ? AND deployment_name = ?`,
		accessLevel, userID, deploymentName)
	return err
}

func (db *DB) DeleteUserDeployment(userID int64, deploymentName string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`DELETE FROM user_deployments WHERE user_id = ? AND deployment_name = ?`,
		userID, deploymentName)
	return err
}

func (db *DB) DeleteAllUserDeployments(userID int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`DELETE FROM user_deployments WHERE user_id = ?`, userID)
	return err
}
