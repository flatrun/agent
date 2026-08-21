package security

import (
	"strings"
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

func TestIngestEventRecordsRequestsDeniedByExistingBlock(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.BlockIP("203.0.113.11", "Manual block", 0); err != nil {
		t.Fatalf("BlockIP: %v", err)
	}

	result, err := m.IngestEvent(&IngestEvent{
		SourceIP:       "203.0.113.11",
		RequestPath:    "/status",
		RequestMethod:  "GET",
		StatusCode:     403,
		UserAgent:      "Mozilla/5.0",
		DeploymentName: "example",
	}, time.Hour)
	if err != nil {
		t.Fatalf("IngestEvent: %v", err)
	}
	if result.Event == nil {
		t.Fatal("expected denied request to be recorded")
	}
	if result.AutoBlocked {
		t.Fatal("existing block must not be extended by a denied request")
	}
}

func TestIngestEventCanBeFoundByIncidentID(t *testing.T) {
	m := newTestManager(t)
	const incidentID = "FR-1234ABCDEF56"
	result, err := m.IngestEvent(&IngestEvent{
		IncidentID:     incidentID,
		SourceIP:       "203.0.113.21",
		RequestPath:    "/checkout",
		RequestMethod:  "GET",
		StatusCode:     502,
		UserAgent:      "Mozilla/5.0",
		DeploymentName: "shop.example.com",
	}, time.Hour)
	if err != nil {
		t.Fatalf("IngestEvent: %v", err)
	}
	if result.Event == nil {
		t.Fatal("expected nginx error to be recorded")
	}

	event, err := m.GetEventByIncidentID(incidentID)
	if err != nil {
		t.Fatalf("GetEventByIncidentID: %v", err)
	}
	if event.IncidentID != incidentID || event.RequestPath != "/checkout" {
		t.Fatalf("unexpected incident: %#v", event)
	}
}

func TestGetActiveBlockedIPsExcludesExpiredRecords(t *testing.T) {
	m := newTestManager(t)
	expiresAt := time.Now().Add(-time.Hour)
	if _, err := m.db.BlockIP("203.0.113.12", "Expired", &expiresAt, true); err != nil {
		t.Fatalf("BlockIP: %v", err)
	}

	ips, err := m.GetActiveBlockedIPs()
	if err != nil {
		t.Fatalf("GetActiveBlockedIPs: %v", err)
	}
	for _, blocked := range ips {
		if blocked.IP == "203.0.113.12" {
			t.Fatal("expired record returned as active")
		}
	}
}

// A legitimate client that identifies as a general-purpose HTTP tool must not be
// blocked on its first request. This reproduces the reported bug where one 404
// from curl / a Go or Python script locked the caller out immediately.
func TestIngestEventDoesNotBlockGenericHTTPClient(t *testing.T) {
	for _, ua := range []string{"curl/8.4.0", "Go-http-client/1.1", "python-requests/2.31.0", "Wget/1.21"} {
		m := newTestManager(t)
		result, err := m.IngestEvent(&IngestEvent{
			SourceIP:      "203.0.113.20",
			RequestPath:   "/wp-login.php",
			RequestMethod: "GET",
			StatusCode:    404,
			UserAgent:     ua,
		}, time.Hour)
		if err != nil {
			t.Fatalf("IngestEvent(%s): %v", ua, err)
		}
		if result.AutoBlocked {
			t.Errorf("UA %q: a single 404 must not auto-block a general-purpose client", ua)
		}
	}
}

// A tool that names itself as an attack scanner is still blocked on sight.
func TestIngestEventBlocksNamedScanner(t *testing.T) {
	m := newTestManager(t)
	result, err := m.IngestEvent(&IngestEvent{
		SourceIP:      "203.0.113.21",
		RequestPath:   "/",
		RequestMethod: "GET",
		StatusCode:    200,
		UserAgent:     "sqlmap/1.7.2#stable",
	}, time.Hour)
	if err != nil {
		t.Fatalf("IngestEvent: %v", err)
	}
	if !result.AutoBlocked {
		t.Fatal("a named scanner user agent must still be auto-blocked")
	}
}

// The persisted block records what tripped it: the rule, the count, and the paths.
func TestIngestEventBlockReasonTracesTrigger(t *testing.T) {
	m := newTestManager(t)

	var last *IngestResult
	for i := 0; i < 10; i++ {
		var err error
		last, err = m.IngestEvent(&IngestEvent{
			SourceIP:      "203.0.113.22",
			RequestPath:   "/missing.php",
			RequestMethod: "GET",
			StatusCode:    404,
			UserAgent:     "Mozilla/5.0",
		}, time.Hour)
		if err != nil {
			t.Fatalf("IngestEvent: %v", err)
		}
	}
	if !last.AutoBlocked {
		t.Fatal("expected block after repeated 404 probing")
	}

	blocked, err := m.GetBlockedIPs()
	if err != nil {
		t.Fatalf("GetBlockedIPs: %v", err)
	}
	var reason string
	for _, b := range blocked {
		if b.IP == "203.0.113.22" {
			reason = b.Reason
		}
	}
	if reason == "" {
		t.Fatal("blocked IP not found")
	}
	for _, want := range []string{"not-found", "/missing.php"} {
		if !strings.Contains(reason, want) {
			t.Errorf("block reason should trace the trigger, missing %q in %q", want, reason)
		}
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
