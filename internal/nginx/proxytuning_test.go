package nginx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
)

func site(mutate func(*models.DomainConfig)) models.DomainConfig {
	d := models.DomainConfig{ID: "site", Service: "web", ContainerPort: 80, Domain: "shop.example.com"}
	mutate(&d)
	return d
}

func TestProxyBufferingOffStreamsResponses(t *testing.T) {
	off := false
	out := renderDomains(t, &config.NginxConfig{}, site(func(d *models.DomainConfig) { d.ProxyBuffering = &off }))
	if !strings.Contains(locationBlock(t, out, "/"), "proxy_buffering off;") {
		t.Fatalf("buffering was turned off:\n%s", out)
	}
	if out := renderDomains(t, &config.NginxConfig{}, site(func(*models.DomainConfig) {})); strings.Contains(out, "proxy_buffering") {
		t.Fatalf("buffering was not set, so nginx's default must hold:\n%s", out)
	}
}

func TestResponseHeadersKeepTheDefaultSecurityHeaders(t *testing.T) {
	out := renderDomains(t, &config.NginxConfig{}, site(func(d *models.DomainConfig) {
		d.ResponseHeaders = map[string]string{"X-Frame-Options": "DENY", "Content-Security-Policy": `default-src 'self'; img-src "data:"`}
	}))
	loc := locationBlock(t, out, "/")
	for _, want := range []string{
		`add_header X-Frame-Options "DENY" always;`,
		`add_header Content-Security-Policy "default-src 'self'; img-src \"data:\"" always;`,
		`add_header X-Content-Type-Options "nosniff" always;`,
		`add_header Referrer-Policy "strict-origin-when-cross-origin" always;`,
	} {
		if !strings.Contains(loc, want) {
			t.Errorf("missing %s in:\n%s", want, loc)
		}
	}
	if strings.Contains(loc, "SAMEORIGIN") {
		t.Errorf("the domain's X-Frame-Options must replace the default:\n%s", loc)
	}
}

func TestAllowListRepeatsBlockedAddresses(t *testing.T) {
	m := NewManager(&config.NginxConfig{}, t.TempDir(), "")
	out, err := m.renderMultiDomainConfig(&models.Deployment{Name: "shop", Metadata: &models.ServiceMetadata{
		Domains:  []models.DomainConfig{site(func(d *models.DomainConfig) { d.AllowIPs = []string{"10.0.0.0/8", "192.0.2.7"} })},
		Security: &models.DeploymentSecurityConfig{Enabled: true, BlockedIPs: []string{"10.1.2.3"}},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	loc := locationBlock(t, out, "/")
	want := "deny 10.1.2.3;\n        allow 10.0.0.0/8;\n        allow 192.0.2.7;\n        deny all;"
	if !strings.Contains(loc, want) {
		t.Fatalf("a blocked address inside an allowed range must still be refused:\n%s", loc)
	}
}

func TestCanonicalRedirectSendsAliasesToThePrimary(t *testing.T) {
	m := NewManager(&config.NginxConfig{}, t.TempDir(), "")
	m.SetCertificateChecker(func(domain string) bool { return domain == "www.shop.example.com" })
	out, err := m.renderMultiDomainConfig(&models.Deployment{Name: "shop", Metadata: &models.ServiceMetadata{Domains: []models.DomainConfig{
		site(func(d *models.DomainConfig) {
			d.Aliases = []string{"www.shop.example.com", "old.example.com"}
			d.CanonicalRedirect = models.CanonicalRedirectPrimary
			d.SSL = models.SSLConfig{Enabled: true, AutoCert: true}
		}),
	}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "server_name shop.example.com www") || strings.Contains(out, "server_name shop.example.com old") {
		t.Errorf("redirected aliases must not be served by the app:\n%s", out)
	}
	if strings.Count(out, "return 301 https://shop.example.com$request_uri;") != 3 {
		t.Errorf("both aliases redirect on 80 and the one with a certificate on 443:\n%s", out)
	}
	if !strings.Contains(out, "ssl_certificate /etc/letsencrypt/live/www.shop.example.com/fullchain.pem;") {
		t.Errorf("the 443 redirect must present the alias's own certificate:\n%s", out)
	}
	if strings.Contains(out, "live/old.example.com/") {
		t.Errorf("an alias without a certificate must not listen on 443:\n%s", out)
	}
}

func TestBasicAuthWritesHashesAndRemovesStaleFiles(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(&config.NginxConfig{ConfigPath: dir}, dir, "")
	m.testConfig = func() error { return nil }
	m.reload = func() error { return nil }
	hash, _ := models.HashBasicAuthPassword("s3cret")
	auth := &models.BasicAuthConfig{Enabled: true, Users: []models.BasicAuthUser{{Username: "alice", PasswordHash: hash}}}
	deployment := &models.Deployment{Name: "shop", Metadata: &models.ServiceMetadata{Domains: []models.DomainConfig{
		site(func(d *models.DomainConfig) { d.BasicAuth = auth }),
	}}}

	if err := m.ApplyDeployment(deployment); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, htpasswdDir, "shop", "site")
	content, err := os.ReadFile(file)
	if err != nil || string(content) != "alice:"+hash+"\n" {
		t.Fatalf("htpasswd = %q, %v", content, err)
	}
	vhost, _ := os.ReadFile(filepath.Join(dir, "shop.conf"))
	if !strings.Contains(string(vhost), "auth_basic_user_file /etc/nginx/conf.d/htpasswd/shop/site;") {
		t.Fatalf("vhost must point at the file inside the proxy container:\n%s", vhost)
	}

	deployment.Metadata.Domains[0].BasicAuth = nil
	if err := m.ApplyDeployment(deployment); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("turning basic auth off must remove the user file, stat err = %v", err)
	}
}

func TestRejectedBasicAuthChangeRestoresTheUserFile(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(&config.NginxConfig{ConfigPath: dir}, dir, "")
	m.testConfig = func() error { return nil }
	m.reload = func() error { return nil }
	first, _ := models.HashBasicAuthPassword("one")
	deployment := &models.Deployment{Name: "shop", Metadata: &models.ServiceMetadata{Domains: []models.DomainConfig{
		site(func(d *models.DomainConfig) {
			d.BasicAuth = &models.BasicAuthConfig{Enabled: true, Users: []models.BasicAuthUser{{Username: "alice", PasswordHash: first}}}
		}),
	}}}
	if err := m.ApplyDeployment(deployment); err != nil {
		t.Fatal(err)
	}

	m.testConfig = func() error { return os.ErrInvalid }
	second, _ := models.HashBasicAuthPassword("two")
	deployment.Metadata.Domains[0].BasicAuth.Users[0].PasswordHash = second
	if err := m.ApplyDeployment(deployment); err == nil {
		t.Fatal("a rejected config must be reported")
	}
	content, _ := os.ReadFile(filepath.Join(dir, htpasswdDir, "shop", "site"))
	if string(content) != "alice:"+first+"\n" {
		t.Fatalf("the previous password must stay in force, got %q", content)
	}
}
