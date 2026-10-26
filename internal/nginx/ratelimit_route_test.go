package nginx

import (
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
)

func rateLimitedDeployment(path string) *models.Deployment {
	return &models.Deployment{
		Name: "shop",
		Metadata: &models.ServiceMetadata{
			Domains: []models.DomainConfig{
				{ID: "d1", Service: "api", ContainerPort: 8080, Domain: "shop.example.com"},
			},
			Security: &models.DeploymentSecurityConfig{
				Enabled:    true,
				RateLimits: []models.DeploymentRateLimit{{Path: path, Rate: 60, Burst: 10, Enabled: true}},
			},
		},
	}
}

func locationBlock(t *testing.T, config, path string) string {
	t.Helper()
	start := strings.Index(config, "location "+path+" {")
	if start == -1 {
		t.Fatalf("no location %s in:\n%s", path, config)
	}
	end := strings.Index(config[start:], "\n    }")
	return config[start : start+end]
}

func TestRateLimitedPathUsesTheRouteUpstream(t *testing.T) {
	m := NewManager(&config.NginxConfig{}, t.TempDir(), "")
	config, err := m.renderMultiDomainConfig(rateLimitedDeployment("/login"), false)
	if err != nil {
		t.Fatal(err)
	}

	block := locationBlock(t, config, "/login")
	if !strings.Contains(block, "limit_req zone=shop_login burst=10 nodelay;") {
		t.Errorf("limited location has no limit_req:\n%s", block)
	}
	if !strings.Contains(block, "set $upstream shop-api:8080;") {
		t.Errorf("limited location must proxy to the route's service and port:\n%s", block)
	}
	if strings.Contains(config, "shop:80") {
		t.Errorf("no location may fall back to <deployment>:80:\n%s", config)
	}
}

func TestRateLimitOnRoutePathIsAddedToThatLocation(t *testing.T) {
	m := NewManager(&config.NginxConfig{}, t.TempDir(), "")
	config, err := m.renderMultiDomainConfig(rateLimitedDeployment("/"), false)
	if err != nil {
		t.Fatal(err)
	}

	if n := strings.Count(config, "location / {"); n != 1 {
		t.Fatalf("location / appears %d times, nginx rejects duplicates:\n%s", n, config)
	}
	if !strings.Contains(locationBlock(t, config, "/"), "limit_req zone=shop_default") {
		t.Errorf("route location has no limit:\n%s", config)
	}
}

func TestHTTPSServerKeepsGlobalSecurityHeaders(t *testing.T) {
	m := NewManager(&config.NginxConfig{}, t.TempDir(), "")
	deployment := &models.Deployment{
		Name: "shop",
		Metadata: &models.ServiceMetadata{
			Domains: []models.DomainConfig{
				{ID: "d1", Service: "web", ContainerPort: 80, Domain: "shop.example.com", SSL: models.SSLConfig{Enabled: true}},
			},
		},
	}
	config, err := m.renderMultiDomainConfig(deployment, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"Strict-Transport-Security", "X-Frame-Options", "X-Content-Type-Options", "X-XSS-Protection", "Referrer-Policy"} {
		if !strings.Contains(config, "add_header "+header+" ") {
			t.Errorf("HTTPS server is missing %s:\n%s", header, config)
		}
	}
}
