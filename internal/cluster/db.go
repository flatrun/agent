package cluster

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Peer struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	URL             string    `json:"url"`
	APIKeyHash      string    `json:"-"`
	APIKeyEncrypted string    `json:"-"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
	LastSeenAt      time.Time `json:"last_seen_at,omitempty"`
}

type Invite struct {
	ID           int64     `json:"id"`
	TokenHash    string    `json:"-"`
	Status       string    `json:"status"`
	CreatedBy    int64     `json:"created_by"`
	AcceptedPeer string    `json:"accepted_peer,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	CreatedAt    time.Time `json:"created_at"`
}

type DB struct {
	conn *sql.DB
	path string
	mu   sync.RWMutex
}

func NewDB(deploymentsPath string) (*DB, error) {
	dbDir := filepath.Join(deploymentsPath, ".flatrun")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, err
	}

	dbPath := filepath.Join(dbDir, "cluster.db")
	conn, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
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
	CREATE TABLE IF NOT EXISTS peers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT UNIQUE NOT NULL,
		url TEXT NOT NULL,
		api_key_hash TEXT NOT NULL,
		api_key_encrypted TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		last_seen_at DATETIME
	);

	CREATE TABLE IF NOT EXISTS invites (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		token_hash TEXT UNIQUE NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		created_by INTEGER NOT NULL,
		accepted_peer TEXT,
		expires_at DATETIME NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_peers_name ON peers(name);
	CREATE INDEX IF NOT EXISTS idx_peers_status ON peers(status);
	CREATE INDEX IF NOT EXISTS idx_invites_token_hash ON invites(token_hash);
	CREATE INDEX IF NOT EXISTS idx_invites_status ON invites(status);
	`

	_, err := db.conn.Exec(schema)
	return err
}

func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func (db *DB) CreatePeer(peer *Peer) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	result, err := db.conn.Exec(`
		INSERT INTO peers (name, url, api_key_hash, api_key_encrypted, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		peer.Name, peer.URL, peer.APIKeyHash, peer.APIKeyEncrypted, peer.Status, time.Now(),
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (db *DB) GetPeer(name string) (*Peer, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var p Peer
	var lastSeen sql.NullTime

	err := db.conn.QueryRow(`
		SELECT id, name, url, api_key_hash, api_key_encrypted, status, created_at, last_seen_at
		FROM peers WHERE name = ?`, name).Scan(
		&p.ID, &p.Name, &p.URL, &p.APIKeyHash, &p.APIKeyEncrypted, &p.Status, &p.CreatedAt, &lastSeen,
	)
	if err != nil {
		return nil, err
	}

	if lastSeen.Valid {
		p.LastSeenAt = lastSeen.Time
	}
	return &p, nil
}

func (db *DB) ListPeers() ([]Peer, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	rows, err := db.conn.Query(`
		SELECT id, name, url, api_key_hash, api_key_encrypted, status, created_at, last_seen_at
		FROM peers ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var peers []Peer
	for rows.Next() {
		var p Peer
		var lastSeen sql.NullTime

		if err := rows.Scan(
			&p.ID, &p.Name, &p.URL, &p.APIKeyHash, &p.APIKeyEncrypted, &p.Status, &p.CreatedAt, &lastSeen,
		); err != nil {
			return nil, err
		}

		if lastSeen.Valid {
			p.LastSeenAt = lastSeen.Time
		}
		peers = append(peers, p)
	}
	return peers, nil
}

func (db *DB) DeletePeer(name string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`DELETE FROM peers WHERE name = ?`, name)
	return err
}

func (db *DB) UpdateLastSeen(name string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`UPDATE peers SET last_seen_at = ? WHERE name = ?`, time.Now(), name)
	return err
}

func (db *DB) CreateInvite(invite *Invite) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	result, err := db.conn.Exec(`
		INSERT INTO invites (token_hash, status, created_by, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		invite.TokenHash, invite.Status, invite.CreatedBy, invite.ExpiresAt, time.Now(),
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (db *DB) GetInviteByHash(tokenHash string) (*Invite, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var inv Invite
	var acceptedPeer sql.NullString

	err := db.conn.QueryRow(`
		SELECT id, token_hash, status, created_by, accepted_peer, expires_at, created_at
		FROM invites WHERE token_hash = ?`, tokenHash).Scan(
		&inv.ID, &inv.TokenHash, &inv.Status, &inv.CreatedBy, &acceptedPeer, &inv.ExpiresAt, &inv.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	inv.AcceptedPeer = acceptedPeer.String
	return &inv, nil
}

func (db *DB) ConsumeInvite(tokenHash string, peerName string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	result, err := db.conn.Exec(`
		UPDATE invites SET status = 'accepted', accepted_peer = ?
		WHERE token_hash = ? AND status = 'pending' AND expires_at > ?`,
		peerName, tokenHash, time.Now(),
	)
	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}
