package setup

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

type SetupState struct {
	ID             int64
	Initialized    bool
	InitializedAt  time.Time
	JWTSecret      string
	InstanceIP     string
	CloudProvider  string
	DeploymentMode string
	UIOrigin       string
	Domain         string
	AutoSSL        bool
	CreatedAt      time.Time
}

func NewSetupDB(deploymentsPath string) (*DB, error) {
	dbDir := filepath.Join(deploymentsPath, ".flatrun")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, err
	}

	dbPath := filepath.Join(dbDir, "setup.db")
	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}

	conn.SetMaxOpenConns(5)
	conn.SetMaxIdleConns(2)
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
	CREATE TABLE IF NOT EXISTS setup_state (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		initialized BOOLEAN DEFAULT FALSE,
		initialized_at DATETIME,
		jwt_secret TEXT,
		instance_ip TEXT,
		cloud_provider TEXT,
		deployment_mode TEXT,
		ui_origin TEXT,
		domain TEXT,
		auto_ssl BOOLEAN DEFAULT FALSE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	INSERT OR IGNORE INTO setup_state (id, initialized, created_at) VALUES (1, FALSE, CURRENT_TIMESTAMP);
	`

	_, err := db.conn.Exec(schema)
	return err
}

func (db *DB) GetState() (*SetupState, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var state SetupState
	var initializedAt sql.NullTime
	var jwtSecret, instanceIP, cloudProvider, deploymentMode, uiOrigin, domain sql.NullString
	var autoSSL sql.NullBool

	err := db.conn.QueryRow(`
		SELECT id, initialized, initialized_at, jwt_secret, instance_ip, cloud_provider,
		       deployment_mode, ui_origin, domain, auto_ssl, created_at
		FROM setup_state WHERE id = 1`).Scan(
		&state.ID, &state.Initialized, &initializedAt, &jwtSecret, &instanceIP,
		&cloudProvider, &deploymentMode, &uiOrigin, &domain, &autoSSL, &state.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	if initializedAt.Valid {
		state.InitializedAt = initializedAt.Time
	}
	state.JWTSecret = jwtSecret.String
	state.InstanceIP = instanceIP.String
	state.CloudProvider = cloudProvider.String
	state.DeploymentMode = deploymentMode.String
	state.UIOrigin = uiOrigin.String
	state.Domain = domain.String
	if autoSSL.Valid {
		state.AutoSSL = autoSSL.Bool
	}

	return &state, nil
}

func (db *DB) SetDeploymentMode(mode string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`UPDATE setup_state SET deployment_mode = ? WHERE id = 1`, mode)
	return err
}

func (db *DB) SetJWTSecret(secret string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`UPDATE setup_state SET jwt_secret = ? WHERE id = 1`, secret)
	return err
}

func (db *DB) SetInstanceIP(ip string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`UPDATE setup_state SET instance_ip = ? WHERE id = 1`, ip)
	return err
}

func (db *DB) SetCloudProvider(provider string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`UPDATE setup_state SET cloud_provider = ? WHERE id = 1`, provider)
	return err
}

func (db *DB) SetUIOrigin(origin string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`UPDATE setup_state SET ui_origin = ? WHERE id = 1`, origin)
	return err
}

func (db *DB) SetDomain(domain string, autoSSL bool) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`UPDATE setup_state SET domain = ?, auto_ssl = ? WHERE id = 1`, domain, autoSSL)
	return err
}

func (db *DB) MarkInitialized() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`UPDATE setup_state SET initialized = TRUE, initialized_at = ? WHERE id = 1`, time.Now())
	return err
}
