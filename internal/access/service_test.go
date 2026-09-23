package access

import (
	"testing"
	"time"

	"github.com/flatrun/agent/pkg/models"
)

func TestResolveUsesTheMostSpecificProtectedPath(t *testing.T) {
	deployments := []models.Deployment{{Metadata: &models.ServiceMetadata{Domains: []models.DomainConfig{
		{Domain: "app.example.com", PathPrefix: "/", Access: &models.DomainAccessConfig{Enabled: true, Mode: "any_verified"}},
		{Domain: "app.example.com", PathPrefix: "/admin", Access: &models.DomainAccessConfig{Enabled: true, Mode: "allowlist", AllowedEmails: []string{"admin@example.com"}}},
	}}}}

	policy, ok := Resolve(deployments, "app.example.com", "/admin/users")
	if !ok || policy.Mode != "allowlist" {
		t.Fatalf("Resolve() = %#v, %v", policy, ok)
	}
	if Allows(policy, "visitor@example.com") {
		t.Fatal("visitor unexpectedly passed the admin allowlist")
	}
}

func TestMagicLinkCreatesAHostBoundSession(t *testing.T) {
	base := t.TempDir()
	service, err := New(base)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	link, err := service.MagicLink("Person@Example.com", "app.example.com", "/private")
	if err != nil {
		t.Fatal(err)
	}
	email, host, returnPath, err := service.VerifyMagicLink(link)
	if err != nil || email != "person@example.com" || host != "app.example.com" || returnPath != "/private" {
		t.Fatalf("VerifyMagicLink() = %q, %q, %q, %v", email, host, returnPath, err)
	}
	if _, _, _, err := service.VerifyMagicLink(link); err == nil {
		t.Fatal("magic link was accepted twice")
	}
	restarted, err := New(base)
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = service.now
	if _, _, _, err := restarted.VerifyMagicLink(link); err == nil {
		t.Fatal("magic link was accepted after restart")
	}
	session, err := service.Session(email, host, 24)
	if err != nil {
		t.Fatal(err)
	}
	policy := &models.DomainAccessConfig{Enabled: true, Mode: "allowlist", AllowedEmails: []string{"person@example.com"}}
	if !service.ValidateSession(session, host, policy) {
		t.Fatal("session was not accepted for its host and policy")
	}
	if service.ValidateSession(session, "other.example.com", policy) {
		t.Fatal("session was accepted for another host")
	}
}

func TestAnyVerifiedPolicyRequiresOneValidEmailAddress(t *testing.T) {
	policy := &models.DomainAccessConfig{Enabled: true, Mode: "any_verified"}
	if !Allows(policy, "person@example.com") {
		t.Fatal("valid email was rejected")
	}
	for _, invalid := range []string{"", "person@example.com,other@example.com", "Person <person@example.com>"} {
		if Allows(policy, invalid) {
			t.Fatalf("invalid email %q was accepted", invalid)
		}
	}
}

func TestAllowlistAcceptsExactEmailDomains(t *testing.T) {
	policy := &models.DomainAccessConfig{
		Enabled:       true,
		Mode:          "allowlist",
		AllowedEmails: []string{"@flatrun.dev", "@WhileSmart.dev"},
	}
	for _, email := range []string{"person@flatrun.dev", "admin@whilesmart.dev"} {
		if !Allows(policy, email) {
			t.Fatalf("domain member %q was rejected", email)
		}
	}
	for _, email := range []string{"person@sub.flatrun.dev", "person@notflatrun.dev", "person@example.com"} {
		if Allows(policy, email) {
			t.Fatalf("non-member %q was accepted", email)
		}
	}
	for _, entry := range []string{"@flatrun.dev", "@WhileSmart.dev", "person@example.com"} {
		if !ValidAllowlistEntry(entry) {
			t.Fatalf("allowlist entry %q was rejected", entry)
		}
	}
	for _, entry := range []string{"@", "@flatrun.dev@example.com", "flatrun.dev"} {
		if ValidAllowlistEntry(entry) {
			t.Fatalf("invalid allowlist entry %q was accepted", entry)
		}
	}
}

func TestMatchingRouteOnlyAliasDoesNotModifyAliases(t *testing.T) {
	aliases := make([]string, 1, 2)
	aliases[0] = "www.example.com"
	backing := aliases[:2]
	backing[1] = "keep.example.com"
	domain := models.DomainConfig{
		Domain:           "example.com",
		Aliases:          aliases,
		RouteOnlyAliases: []string{"internal.example.com"},
	}

	if !matchesHost(domain, "internal.example.com") {
		t.Fatal("route-only alias did not match")
	}
	if backing[1] != "keep.example.com" {
		t.Fatalf("alias backing array was modified: %q", backing[1])
	}
}

func TestAllowEmailRequestEvictsExpiredEntries(t *testing.T) {
	service, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	service.now = func() time.Time { return now }
	service.AllowEmailRequest("app.example.com", "first@example.com")
	now = now.Add(time.Minute)
	service.AllowEmailRequest("app.example.com", "second@example.com")
	if len(service.lastRequests) != 1 {
		t.Fatalf("rate limit entries = %d", len(service.lastRequests))
	}
}

func TestCanonicalAccessValues(t *testing.T) {
	if Hostname("APP.EXAMPLE.COM.:443") != "app.example.com" {
		t.Fatal("host with port was not normalized")
	}
	if Hostname("APP.EXAMPLE.COM.") != "app.example.com" {
		t.Fatal("trailing dot was not removed")
	}
	if SafeReturn("//other.example.com") != "/" {
		t.Fatal("unsafe return path was accepted")
	}
}
