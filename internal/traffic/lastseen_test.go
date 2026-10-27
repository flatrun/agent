package traffic

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLastSeen(t *testing.T) {
	m, err := NewManager(t.TempDir(), 7)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if last, err := m.LastSeen("app"); err != nil || !last.IsZero() {
		t.Fatalf("no traffic = %v, %v", last, err)
	}
	for _, path := range []string{"/a", "/b"} {
		if _, err := m.IngestLog(&IngestTrafficLog{DeploymentName: "app", RequestPath: path, RequestMethod: "GET", StatusCode: 200}); err != nil {
			t.Fatal(err)
		}
	}
	last, err := m.LastSeen("app")
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(last) > time.Minute {
		t.Errorf("LastSeen = %v, want just now", last)
	}
}

func TestLastSeenServiceUsesTheRoutedServiceOnAnOlderDatabase(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".flatrun"), 0755); err != nil {
		t.Fatal(err)
	}
	old, err := sql.Open("sqlite", "file:"+filepath.Join(dir, ".flatrun", "traffic.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE traffic_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, deployment_name TEXT NOT NULL,
		request_path TEXT, request_method TEXT, status_code INTEGER, source_ip TEXT, response_time_ms INTEGER, bytes_sent INTEGER,
		request_length INTEGER, upstream_time_ms INTEGER, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	m, err := NewManager(dir, 7)
	if err != nil {
		t.Fatalf("an existing database must be upgraded in place: %v", err)
	}
	defer m.Close()

	if _, err := m.IngestLog(&IngestTrafficLog{DeploymentName: "shop.example.com", RoutedDeployment: "shop", RoutedService: "web", StatusCode: 200}); err != nil {
		t.Fatal(err)
	}
	if last, _ := m.LastSeenService("shop", "web"); time.Since(last) > time.Minute {
		t.Errorf("web LastSeen = %v, want just now", last)
	}
	if last, _ := m.LastSeenService("shop", "worker"); !last.IsZero() {
		t.Errorf("worker had no requests, LastSeen = %v", last)
	}
	if last, _ := m.LastSeen("shop"); time.Since(last) > time.Minute {
		t.Errorf("a deployment not named after its host must still see its traffic, LastSeen = %v", last)
	}
}
