package traffic

import (
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
