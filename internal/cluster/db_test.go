package cluster

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setupTestDB(t *testing.T) (*DB, func()) {
	tmpDir, err := os.MkdirTemp("", "cluster_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	db, err := NewDB(tmpDir)
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

func TestNewDB(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	if db == nil {
		t.Fatal("NewDB returned nil")
	}
	if db.conn == nil {
		t.Fatal("DB connection is nil")
	}
}

func TestNewDBRecordsSchemaVersion(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	var version int64
	if err := db.conn.QueryRow(`SELECT MAX(version_id) FROM cluster_schema_version WHERE is_applied = 1`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != 2 {
		t.Fatalf("schema version = %d, want 2", version)
	}
}

func TestNewDBMigratesExistingPeersWithDefaultPolicy(t *testing.T) {
	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, ".flatrun")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatalf("create database directory: %v", err)
	}
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dbDir, "cluster.db"))
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	_, err = conn.Exec(`
		CREATE TABLE peers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT UNIQUE NOT NULL,
			url TEXT NOT NULL,
			api_key_hash TEXT NOT NULL,
			api_key_encrypted TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_seen_at DATETIME
		);
		CREATE TABLE invites (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			token_hash TEXT UNIQUE NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			created_by INTEGER NOT NULL,
			accepted_peer TEXT,
			expires_at DATETIME NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO peers (name, url, api_key_hash, api_key_encrypted)
		VALUES ('prod-2', 'https://prod-2.example.com', 'hash', 'encrypted');
	`)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("seed legacy database: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	db, err := NewDB(tmpDir)
	if err != nil {
		t.Fatalf("migrate legacy database: %v", err)
	}
	defer db.Close()

	peer, err := db.GetPeer("prod-2")
	if err != nil {
		t.Fatalf("read migrated peer: %v", err)
	}
	if peer.URL != "https://prod-2.example.com" {
		t.Fatalf("peer URL = %q", peer.URL)
	}
	policy, err := db.GetPeerPolicy("prod-2")
	if err != nil {
		t.Fatalf("read migrated peer policy: %v", err)
	}
	if len(policy.Grants) != len(DefaultPeerGrants()) {
		t.Fatalf("policy grants = %#v", policy.Grants)
	}
}

func TestNewDBRepairsMissingPeerPolicy(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := NewDB(tmpDir)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	peer := &Peer{
		Name:            "prod-2",
		URL:             "https://prod-2.example.com",
		APIKeyHash:      "hash",
		APIKeyEncrypted: "encrypted",
		Status:          "active",
	}
	if _, err := db.CreatePeer(peer); err != nil {
		t.Fatalf("create peer: %v", err)
	}
	if _, err := db.conn.Exec(`DELETE FROM peer_policies WHERE peer_name = ?`, peer.Name); err != nil {
		t.Fatalf("remove peer policy: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	db, err = NewDB(tmpDir)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer db.Close()
	policy, err := db.GetPeerPolicy(peer.Name)
	if err != nil {
		t.Fatalf("read repaired peer policy: %v", err)
	}
	if len(policy.Grants) != len(DefaultPeerGrants()) {
		t.Fatalf("policy grants = %#v", policy.Grants)
	}
}

func TestDBPath(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cluster_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	db, err := NewDB(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	expectedPath := filepath.Join(tmpDir, ".flatrun", "cluster.db")
	if db.path != expectedPath {
		t.Errorf("DB path = %s, want %s", db.path, expectedPath)
	}

	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Error("Database file was not created")
	}
}

func TestCreateAndGetPeer(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	peer := &Peer{
		Name:            "hetzner-1",
		URL:             "https://hetzner-1.example.com:8090",
		APIKeyHash:      HashToken("test-api-key"),
		APIKeyEncrypted: "encrypted-key-data",
		Status:          "active",
	}

	id, err := db.CreatePeer(peer)
	if err != nil {
		t.Fatalf("CreatePeer failed: %v", err)
	}
	if id <= 0 {
		t.Error("CreatePeer should return positive ID")
	}

	retrieved, err := db.GetPeer("hetzner-1")
	if err != nil {
		t.Fatalf("GetPeer failed: %v", err)
	}

	if retrieved.Name != "hetzner-1" {
		t.Errorf("Name = %s, want hetzner-1", retrieved.Name)
	}
	if retrieved.URL != "https://hetzner-1.example.com:8090" {
		t.Errorf("URL = %s, want https://hetzner-1.example.com:8090", retrieved.URL)
	}
	if retrieved.Status != "active" {
		t.Errorf("Status = %s, want active", retrieved.Status)
	}
}

func TestGetPeerNotFound(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	_, err := db.GetPeer("nonexistent")
	if err == nil {
		t.Error("GetPeer should fail for nonexistent peer")
	}
}

func TestCreatePeerDuplicateName(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	peer := &Peer{
		Name:            "dupe",
		URL:             "https://a.example.com",
		APIKeyHash:      "hash1",
		APIKeyEncrypted: "enc1",
		Status:          "active",
	}
	_, err := db.CreatePeer(peer)
	if err != nil {
		t.Fatalf("First CreatePeer failed: %v", err)
	}

	peer2 := &Peer{
		Name:            "dupe",
		URL:             "https://b.example.com",
		APIKeyHash:      "hash2",
		APIKeyEncrypted: "enc2",
		Status:          "active",
	}
	_, err = db.CreatePeer(peer2)
	if err == nil {
		t.Error("CreatePeer should fail for duplicate name")
	}
}

func TestListPeers(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	peers, err := db.ListPeers()
	if err != nil {
		t.Fatalf("ListPeers failed: %v", err)
	}
	if len(peers) != 0 {
		t.Errorf("Expected 0 peers, got %d", len(peers))
	}

	_, _ = db.CreatePeer(&Peer{
		Name: "peer-a", URL: "https://a.example.com",
		APIKeyHash: "h1", APIKeyEncrypted: "e1", Status: "active",
	})
	_, _ = db.CreatePeer(&Peer{
		Name: "peer-b", URL: "https://b.example.com",
		APIKeyHash: "h2", APIKeyEncrypted: "e2", Status: "active",
	})

	peers, err = db.ListPeers()
	if err != nil {
		t.Fatalf("ListPeers failed: %v", err)
	}
	if len(peers) != 2 {
		t.Errorf("Expected 2 peers, got %d", len(peers))
	}
}

func TestDeletePeer(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	_, _ = db.CreatePeer(&Peer{
		Name: "to-delete", URL: "https://delete.example.com",
		APIKeyHash: "h", APIKeyEncrypted: "e", Status: "active",
	})

	err := db.DeletePeer("to-delete")
	if err != nil {
		t.Fatalf("DeletePeer failed: %v", err)
	}

	_, err = db.GetPeer("to-delete")
	if err == nil {
		t.Error("GetPeer should fail after deletion")
	}
}

func TestPeerPolicyDefaultsAndUpdates(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	_, err := db.CreatePeer(&Peer{
		Name: "policy-peer", URL: "https://peer.example.com",
		APIKeyHash: "h", APIKeyEncrypted: "e", Status: "active",
	})
	if err != nil {
		t.Fatalf("CreatePeer failed: %v", err)
	}

	policy, err := db.GetPeerPolicy("policy-peer")
	if err != nil {
		t.Fatalf("GetPeerPolicy failed: %v", err)
	}
	if len(policy.Grants) != len(DefaultPeerGrants()) {
		t.Fatalf("default grants = %#v", policy.Grants)
	}

	policy.Grants = []Grant{{Capability: CapabilityCapacityOffer, MaxCPU: 2, MaxMemory: 2 << 30, MaxReplicas: 3}}
	if err := db.SetPeerPolicy(*policy); err != nil {
		t.Fatalf("SetPeerPolicy failed: %v", err)
	}
	updated, err := db.GetPeerPolicy("policy-peer")
	if err != nil {
		t.Fatalf("GetPeerPolicy after update failed: %v", err)
	}
	if len(updated.Grants) != 1 || updated.Grants[0].MaxReplicas != 3 {
		t.Fatalf("updated policy = %#v", updated)
	}
}

func TestDeletePeerDeletesPolicy(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	_, _ = db.CreatePeer(&Peer{
		Name: "policy-delete", URL: "https://peer.example.com",
		APIKeyHash: "h", APIKeyEncrypted: "e", Status: "active",
	})
	if err := db.DeletePeer("policy-delete"); err != nil {
		t.Fatalf("DeletePeer failed: %v", err)
	}
	if _, err := db.GetPeerPolicy("policy-delete"); err == nil {
		t.Fatal("policy should be deleted with its peer")
	}
}

func TestUpdateLastSeen(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	_, _ = db.CreatePeer(&Peer{
		Name: "seen-peer", URL: "https://seen.example.com",
		APIKeyHash: "h", APIKeyEncrypted: "e", Status: "active",
	})

	before, _ := db.GetPeer("seen-peer")
	if !before.LastSeenAt.IsZero() {
		t.Error("LastSeenAt should be zero initially")
	}

	err := db.UpdateLastSeen("seen-peer")
	if err != nil {
		t.Fatalf("UpdateLastSeen failed: %v", err)
	}

	after, _ := db.GetPeer("seen-peer")
	if after.LastSeenAt.IsZero() {
		t.Error("LastSeenAt should be set after update")
	}
}

func TestCreateAndGetInvite(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	token := "test-invite-token"
	tokenHash := HashToken(token)

	invite := &Invite{
		TokenHash: tokenHash,
		Status:    "pending",
		CreatedBy: 1,
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	id, err := db.CreateInvite(invite)
	if err != nil {
		t.Fatalf("CreateInvite failed: %v", err)
	}
	if id <= 0 {
		t.Error("CreateInvite should return positive ID")
	}

	retrieved, err := db.GetInviteByHash(tokenHash)
	if err != nil {
		t.Fatalf("GetInviteByHash failed: %v", err)
	}

	if retrieved.Status != "pending" {
		t.Errorf("Status = %s, want pending", retrieved.Status)
	}
	if retrieved.CreatedBy != 1 {
		t.Errorf("CreatedBy = %d, want 1", retrieved.CreatedBy)
	}
}

func TestGetInviteNotFound(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	_, err := db.GetInviteByHash("nonexistent-hash")
	if err == nil {
		t.Error("GetInviteByHash should fail for nonexistent invite")
	}
}

func TestConsumeInvite(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	token := "consume-token"
	tokenHash := HashToken(token)

	_, _ = db.CreateInvite(&Invite{
		TokenHash: tokenHash,
		Status:    "pending",
		CreatedBy: 1,
		ExpiresAt: time.Now().Add(1 * time.Hour),
	})

	err := db.ConsumeInvite(tokenHash, "new-peer")
	if err != nil {
		t.Fatalf("ConsumeInvite failed: %v", err)
	}

	invite, _ := db.GetInviteByHash(tokenHash)
	if invite.Status != "accepted" {
		t.Errorf("Status = %s, want accepted", invite.Status)
	}
	if invite.AcceptedPeer != "new-peer" {
		t.Errorf("AcceptedPeer = %s, want new-peer", invite.AcceptedPeer)
	}
}

func TestConsumeInviteAlreadyAccepted(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	token := "already-used"
	tokenHash := HashToken(token)

	_, _ = db.CreateInvite(&Invite{
		TokenHash: tokenHash,
		Status:    "pending",
		CreatedBy: 1,
		ExpiresAt: time.Now().Add(1 * time.Hour),
	})

	_ = db.ConsumeInvite(tokenHash, "first-peer")

	err := db.ConsumeInvite(tokenHash, "second-peer")
	if err != sql.ErrNoRows {
		t.Errorf("Expected sql.ErrNoRows, got %v", err)
	}
}

func TestConsumeInviteExpired(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	token := "expired-token"
	tokenHash := HashToken(token)

	_, _ = db.CreateInvite(&Invite{
		TokenHash: tokenHash,
		Status:    "pending",
		CreatedBy: 1,
		ExpiresAt: time.Now().Add(-1 * time.Hour),
	})

	err := db.ConsumeInvite(tokenHash, "late-peer")
	if err != sql.ErrNoRows {
		t.Errorf("Expected sql.ErrNoRows for expired invite, got %v", err)
	}
}

func TestHashToken(t *testing.T) {
	hash1 := HashToken("token-a")
	hash2 := HashToken("token-b")
	hash1again := HashToken("token-a")

	if hash1 == hash2 {
		t.Error("Different tokens should produce different hashes")
	}
	if hash1 != hash1again {
		t.Error("Same token should produce same hash")
	}
	if len(hash1) != 64 {
		t.Errorf("Hash length = %d, want 64", len(hash1))
	}
}
