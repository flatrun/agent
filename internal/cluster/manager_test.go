package cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func setupTestManager(t *testing.T) (*Manager, *DB, func()) {
	tmpDir, err := os.MkdirTemp("", "cluster_mgr_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	db, err := NewDB(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create DB: %v", err)
	}

	mgr := NewManager(db, "test-server", 1*time.Second, 5*time.Second, "test-jwt-secret")

	cleanup := func() {
		mgr.Stop()
		db.Close()
		os.RemoveAll(tmpDir)
	}

	return mgr, db, cleanup
}

func TestManagerServerName(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	if mgr.ServerName() != "test-server" {
		t.Errorf("ServerName = %s, want test-server", mgr.ServerName())
	}
}

func TestManagerAddAndGetPeer(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	err := mgr.AddPeer("peer-a", "https://a.example.com", "api-key-for-a")
	if err != nil {
		t.Fatalf("AddPeer failed: %v", err)
	}

	client, err := mgr.GetPeer("peer-a")
	if err != nil {
		t.Fatalf("GetPeer failed: %v", err)
	}
	if client == nil {
		t.Fatal("GetPeer returned nil client")
	}
}

func TestManagerGetPeerNotFound(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	_, err := mgr.GetPeer("nonexistent")
	if err == nil {
		t.Error("GetPeer should fail for nonexistent peer")
	}
}

func TestManagerListPeers(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	peers := mgr.ListPeers()
	if len(peers) != 0 {
		t.Errorf("Expected 0 peers, got %d", len(peers))
	}

	_ = mgr.AddPeer("peer-a", "https://a.example.com", "key-a")
	_ = mgr.AddPeer("peer-b", "https://b.example.com", "key-b")

	peers = mgr.ListPeers()
	if len(peers) != 2 {
		t.Errorf("Expected 2 peers, got %d", len(peers))
	}
}

func TestManagerRemovePeer(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	_ = mgr.AddPeer("remove-me", "https://rm.example.com", "key")

	err := mgr.RemovePeer("remove-me")
	if err != nil {
		t.Fatalf("RemovePeer failed: %v", err)
	}

	_, err = mgr.GetPeer("remove-me")
	if err == nil {
		t.Error("GetPeer should fail after RemovePeer")
	}

	peers := mgr.ListPeers()
	if len(peers) != 0 {
		t.Errorf("Expected 0 peers after removal, got %d", len(peers))
	}
}

func TestManagerEncryptDecryptRoundtrip(t *testing.T) {
	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	original := "my-secret-api-key-12345"
	encrypted, err := mgr.encrypt(original)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	if encrypted == original {
		t.Error("Encrypted should differ from original")
	}

	decrypted, err := mgr.decrypt(encrypted)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}

	if decrypted != original {
		t.Errorf("Decrypted = %s, want %s", decrypted, original)
	}
}

func TestManagerStartLoadsExistingPeers(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cluster_start_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	db, err := NewDB(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}

	mgr1 := NewManager(db, "server-1", 30*time.Second, 5*time.Second, "test-secret")
	_ = mgr1.AddPeer("pre-existing", "https://pre.example.com", "pre-key")
	mgr1.Stop()

	mgr2 := NewManager(db, "server-1", 30*time.Second, 5*time.Second, "test-secret")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = mgr2.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer mgr2.Stop()

	peers := mgr2.ListPeers()
	if len(peers) != 1 {
		t.Fatalf("Expected 1 peer after restart, got %d", len(peers))
	}
	if peers[0].Name != "pre-existing" {
		t.Errorf("Peer name = %s, want pre-existing", peers[0].Name)
	}

	_, err = mgr2.GetPeer("pre-existing")
	if err != nil {
		t.Error("Should be able to get pre-existing peer client after restart")
	}

	db.Close()
}

func TestManagerForEachPeer(t *testing.T) {
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"server": "one"})
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"server": "two"})
	}))
	defer server2.Close()

	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	_ = mgr.AddPeer("server-one", server1.URL, "key1")
	_ = mgr.AddPeer("server-two", server2.URL, "key2")

	results := mgr.ForEachPeer(context.Background(), func(ctx context.Context, name string, client *Client) ([]byte, error) {
		data, _, err := client.Get(ctx, "/api/test")
		return data, err
	})

	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}

	for name, result := range results {
		if result.Error != "" {
			t.Errorf("Peer %s returned error: %s", name, result.Error)
		}
		if result.Data == nil {
			t.Errorf("Peer %s returned nil data", name)
		}
	}
}

func TestManagerForEachPeerWithFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"ok": "true"})
	}))
	defer server.Close()

	mgr, _, cleanup := setupTestManager(t)
	defer cleanup()

	_ = mgr.AddPeer("good", server.URL, "key1")
	_ = mgr.AddPeer("bad", "http://127.0.0.1:1", "key2")

	results := mgr.ForEachPeer(context.Background(), func(ctx context.Context, name string, client *Client) ([]byte, error) {
		data, _, err := client.Get(ctx, "/api/test")
		return data, err
	})

	if results["good"].Error != "" {
		t.Errorf("Good peer should not have error: %s", results["good"].Error)
	}
	if results["bad"].Error == "" {
		t.Error("Bad peer should have error")
	}
}

func TestManagerHealthChecks(t *testing.T) {
	var healthCalls int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			atomic.AddInt64(&healthCalls, 1)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	tmpDir, err := os.MkdirTemp("", "cluster_health_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	db, err := NewDB(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	mgr := NewManager(db, "health-test", 100*time.Millisecond, 5*time.Second, "test-secret")
	_ = mgr.AddPeer("healthy-peer", server.URL, "key")

	ctx, cancel := context.WithCancel(context.Background())
	_ = mgr.Start(ctx)

	time.Sleep(350 * time.Millisecond)

	cancel()
	mgr.Stop()

	calls := atomic.LoadInt64(&healthCalls)
	if calls < 2 {
		t.Errorf("Expected at least 2 health checks, got %d", calls)
	}

	peers := mgr.ListPeers()
	found := false
	for _, p := range peers {
		if p.Name == "healthy-peer" {
			found = true
			if !p.Online {
				t.Error("Healthy peer should be online")
			}
		}
	}
	if !found {
		t.Error("healthy-peer not found in peer list")
	}
}
