package nginx

import (
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
)

func renderDomains(t *testing.T, cfg *config.NginxConfig, domains ...models.DomainConfig) string {
	t.Helper()
	m := NewManager(cfg, t.TempDir(), "")
	out, err := m.renderMultiDomainConfig(&models.Deployment{Name: "shop", Metadata: &models.ServiceMetadata{Domains: domains}}, false)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestUnsetUploadLimitLeavesNginxDefault(t *testing.T) {
	out := renderDomains(t, &config.NginxConfig{}, models.DomainConfig{ID: "site", Service: "web", ContainerPort: 80, Domain: "shop.example.com"})
	if strings.Contains(out, "client_max_body_size") {
		t.Fatalf("no limit was set, so none may be written (a hand-set nginx.conf value must hold):\n%s", out)
	}
	if !strings.Contains(out, "error_page 413 = @flatrun_too_large_site;") || !strings.Contains(out, "location @flatrun_too_large_site {") {
		t.Fatalf("413 must still be routed to the agent:\n%s", out)
	}
}

func TestDomainUploadLimitIsWrittenToItsLocationAndServer(t *testing.T) {
	out := renderDomains(t, &config.NginxConfig{},
		models.DomainConfig{ID: "media", Service: "web", ContainerPort: 80, Domain: "shop.example.com", PathPrefix: "/media", ClientMaxBodySize: "64m"},
		models.DomainConfig{ID: "site", Service: "web", ContainerPort: 80, Domain: "shop.example.com"},
	)
	if !strings.Contains(out, "\n    client_max_body_size 64m;") {
		t.Errorf("server block must carry the largest limit on the host:\n%s", out)
	}
	if !strings.Contains(locationBlock(t, out, "/media"), "client_max_body_size 64m;") {
		t.Errorf("/media must carry its own limit:\n%s", out)
	}
	if !strings.Contains(locationBlock(t, out, "/"), "client_max_body_size 1m;") {
		t.Errorf("/ must stay at the nginx default instead of inheriting 64m from the server:\n%s", out)
	}
}

func TestAgentDefaultUploadLimitAppliesToDomainsWithoutOne(t *testing.T) {
	out := renderDomains(t, &config.NginxConfig{ClientMaxBodySize: "32m"},
		models.DomainConfig{ID: "site", Service: "web", ContainerPort: 80, Domain: "shop.example.com"},
	)
	if !strings.Contains(locationBlock(t, out, "/"), "client_max_body_size 32m;") {
		t.Errorf("agent default not applied:\n%s", out)
	}
}

func TestUnsafeDomainIDGetsNoTooLargeHandler(t *testing.T) {
	out := renderDomains(t, &config.NginxConfig{},
		models.DomainConfig{ID: "bad id;", Service: "web", ContainerPort: 80, Domain: "shop.example.com"},
	)
	if strings.Contains(out, "bad id") || strings.Contains(out, "error_page 413") {
		t.Fatalf("an unsafe ID must never reach the config:\n%s", out)
	}
}

func TestTooLargeHandlerKeepsSleepWake(t *testing.T) {
	m := NewManager(&config.NginxConfig{}, t.TempDir(), "")
	out, err := m.renderMultiDomainConfig(&models.Deployment{Name: "shop", Metadata: &models.ServiceMetadata{
		Sleep:   &models.SleepConfig{Enabled: true, IdleMinutes: 10},
		Domains: []models.DomainConfig{{ID: "site", Service: "web", ContainerPort: 80, Domain: "shop.example.com"}},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(locationBlock(t, out, "/"), "error_page 502 503 504 = @flatrun_wake;") {
		t.Fatalf("a location-level error_page stops nginx inheriting the server's wake handler, so the location must repeat it:\n%s", out)
	}
}
