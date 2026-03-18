package cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestAggregateFromPeers(t *testing.T) {
	peerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]string{
			{"name": "remote-app", "status": "running"},
		})
	}))
	defer peerServer.Close()

	tmpDir, err := os.MkdirTemp("", "cluster_agg_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	db, err := NewDB(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	mgr := NewManager(db, "primary", 30*time.Second, 5*time.Second, "test-secret")
	_ = mgr.AddPeer("remote-server", peerServer.URL, "key")

	localData, _ := json.Marshal([]map[string]string{
		{"name": "local-app", "status": "running"},
	})

	result := AggregateFromPeers(context.Background(), localData, mgr, "/api/deployments")

	if len(result.Servers) != 2 {
		t.Fatalf("Expected 2 servers, got %d", len(result.Servers))
	}

	local, ok := result.Servers["primary"]
	if !ok {
		t.Fatal("Expected primary server in results")
	}
	if !local.Online {
		t.Error("Local server should be online")
	}
	if local.Data == nil {
		t.Error("Local server data should not be nil")
	}

	remote, ok := result.Servers["remote-server"]
	if !ok {
		t.Fatal("Expected remote-server in results")
	}
	if !remote.Online {
		t.Error("Remote server should be online")
	}
	if remote.Data == nil {
		t.Error("Remote server data should not be nil")
	}
}

func TestAggregateFromPeersWithOfflinePeer(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cluster_agg_offline_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	db, err := NewDB(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	mgr := NewManager(db, "primary", 30*time.Second, 1*time.Second, "test-secret")
	_ = mgr.AddPeer("offline-peer", "http://127.0.0.1:1", "key")

	localData, _ := json.Marshal([]string{"local-deployment"})

	result := AggregateFromPeers(context.Background(), localData, mgr, "/api/deployments")

	if len(result.Servers) != 2 {
		t.Fatalf("Expected 2 servers, got %d", len(result.Servers))
	}

	offline, ok := result.Servers["offline-peer"]
	if !ok {
		t.Fatal("Expected offline-peer in results")
	}
	if offline.Online {
		t.Error("Offline peer should not be online")
	}
	if offline.Error == "" {
		t.Error("Offline peer should have error")
	}
}

func TestAggregateFromPeersNoPeers(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cluster_agg_none_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	db, err := NewDB(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	mgr := NewManager(db, "solo", 30*time.Second, 5*time.Second, "test-secret")

	localData, _ := json.Marshal(map[string]string{"status": "healthy"})

	result := AggregateFromPeers(context.Background(), localData, mgr, "/api/health")

	if len(result.Servers) != 1 {
		t.Fatalf("Expected 1 server (local only), got %d", len(result.Servers))
	}

	local, ok := result.Servers["solo"]
	if !ok {
		t.Fatal("Expected solo server in results")
	}
	if !local.Online {
		t.Error("Local server should be online")
	}
}

func TestAggregateFromPeersBadStatusCode(t *testing.T) {
	peerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal error"}`))
	}))
	defer peerServer.Close()

	tmpDir, err := os.MkdirTemp("", "cluster_agg_bad_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	db, err := NewDB(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	mgr := NewManager(db, "primary", 30*time.Second, 5*time.Second, "test-secret")
	_ = mgr.AddPeer("error-peer", peerServer.URL, "key")

	localData, _ := json.Marshal([]string{})

	result := AggregateFromPeers(context.Background(), localData, mgr, "/api/deployments")

	errorPeer, ok := result.Servers["error-peer"]
	if !ok {
		t.Fatal("Expected error-peer in results")
	}
	if errorPeer.Online {
		t.Error("Peer that returned 500 should be marked offline")
	}
	if errorPeer.Error == "" {
		t.Error("Peer that returned 500 should have error message")
	}
}
