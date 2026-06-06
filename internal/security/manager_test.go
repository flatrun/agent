package security

import (
	"testing"
	"time"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func ingestAuthFailures(t *testing.T, m *Manager, ip, path string, n int) *IngestResult {
	t.Helper()
	var last *IngestResult
	for i := 0; i < n; i++ {
		var err error
		last, err = m.IngestEvent(&IngestEvent{
			SourceIP:      ip,
			RequestPath:   path,
			RequestMethod: "GET",
			StatusCode:    401,
			UserAgent:     "Mozilla/5.0",
		}, time.Hour)
		if err != nil {
			t.Fatalf("IngestEvent: %v", err)
		}
	}
	return last
}

func TestIngestEventAutoBlocksOnRepeatedAuthFailures(t *testing.T) {
	m := newTestManager(t)

	result := ingestAuthFailures(t, m, "203.0.113.10", "/api/v1/stats", 5)

	if !result.AutoBlocked {
		t.Fatal("expected IP to be auto-blocked after repeated auth failures")
	}
	blocked, err := m.IsIPBlocked("203.0.113.10")
	if err != nil {
		t.Fatalf("IsIPBlocked: %v", err)
	}
	if !blocked {
		t.Fatal("expected IP to be in blocked list")
	}
}

func TestIngestEventSkipsWhitelistedIP(t *testing.T) {
	m := newTestManager(t)

	if _, err := m.AddWhitelistEntry("203.0.113.7", "ip", "test"); err != nil {
		t.Fatalf("AddWhitelistEntry: %v", err)
	}

	result := ingestAuthFailures(t, m, "203.0.113.7", "/api/v1/stats", 20)

	if result.Event != nil {
		t.Fatal("expected no event for whitelisted IP")
	}
	if result.AutoBlocked {
		t.Fatal("expected whitelisted IP to never be auto-blocked")
	}
	blocked, err := m.IsIPBlocked("203.0.113.7")
	if err != nil {
		t.Fatalf("IsIPBlocked: %v", err)
	}
	if blocked {
		t.Fatal("whitelisted IP must not be blocked")
	}
}

func TestIngestEventSkipsIPInWhitelistedCIDR(t *testing.T) {
	m := newTestManager(t)

	if _, err := m.AddWhitelistEntry("198.51.100.0/24", "cidr", "test range"); err != nil {
		t.Fatalf("AddWhitelistEntry: %v", err)
	}

	result := ingestAuthFailures(t, m, "198.51.100.20", "/api/v1/stats", 20)

	if result.Event != nil || result.AutoBlocked {
		t.Fatal("expected IP inside whitelisted CIDR to be skipped")
	}
}

func TestIngestEventSkipsSeededPrivateNetworks(t *testing.T) {
	m := newTestManager(t)

	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "172.18.0.5", "192.168.1.50"} {
		result := ingestAuthFailures(t, m, ip, "/api/v1/stats", 20)
		if result.Event != nil || result.AutoBlocked {
			t.Fatalf("expected default-whitelisted IP %s to be skipped", ip)
		}
	}
}

func TestIngestEventSkipsWhitelistedPathPrefix(t *testing.T) {
	m := newTestManager(t)

	result, err := m.IngestEvent(&IngestEvent{
		SourceIP:      "203.0.113.99",
		RequestPath:   "/api/health",
		RequestMethod: "GET",
		StatusCode:    500,
		UserAgent:     "Mozilla/5.0",
	}, time.Hour)
	if err != nil {
		t.Fatalf("IngestEvent: %v", err)
	}

	if result.Event != nil || result.AutoBlocked {
		t.Fatal("expected request to whitelisted path to be skipped")
	}
}

func TestWhitelistCacheInvalidatedOnMutation(t *testing.T) {
	m := newTestManager(t)

	got, err := m.IsRequestWhitelisted("203.0.113.40", "/api/v1/stats")
	if err != nil {
		t.Fatalf("IsRequestWhitelisted: %v", err)
	}
	if got {
		t.Fatal("IP unexpectedly whitelisted before adding entry")
	}

	id, err := m.AddWhitelistEntry("203.0.113.40", "ip", "test")
	if err != nil {
		t.Fatalf("AddWhitelistEntry: %v", err)
	}
	got, err = m.IsRequestWhitelisted("203.0.113.40", "/api/v1/stats")
	if err != nil {
		t.Fatalf("IsRequestWhitelisted: %v", err)
	}
	if !got {
		t.Fatal("expected entry added after cache build to be honored")
	}

	if err := m.RemoveWhitelistEntry(id); err != nil {
		t.Fatalf("RemoveWhitelistEntry: %v", err)
	}
	got, err = m.IsRequestWhitelisted("203.0.113.40", "/api/v1/stats")
	if err != nil {
		t.Fatalf("IsRequestWhitelisted: %v", err)
	}
	if got {
		t.Fatal("expected removed entry to stop matching")
	}
}

func TestIsRequestWhitelisted(t *testing.T) {
	m := newTestManager(t)

	if _, err := m.AddWhitelistEntry("2001:db8::/32", "cidr", "test v6"); err != nil {
		t.Fatalf("AddWhitelistEntry: %v", err)
	}

	cases := []struct {
		ip   string
		path string
		want bool
	}{
		{"127.0.0.1", "/anything", true},
		{"10.255.0.1", "/anything", true},
		{"2001:db8::1", "/anything", true},
		{"203.0.113.5", "/api/_internal/blocked-ips", true},
		{"203.0.113.5", "/wp-login.php", false},
		{"not-an-ip", "/wp-login.php", false},
	}

	for _, tc := range cases {
		got, err := m.IsRequestWhitelisted(tc.ip, tc.path)
		if err != nil {
			t.Fatalf("IsRequestWhitelisted(%s, %s): %v", tc.ip, tc.path, err)
		}
		if got != tc.want {
			t.Errorf("IsRequestWhitelisted(%s, %s) = %v, want %v", tc.ip, tc.path, got, tc.want)
		}
	}
}
