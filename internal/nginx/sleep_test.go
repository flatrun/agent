package nginx

import (
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
)

func sleepDeployment(sleep bool, domains []models.DomainConfig) *models.Deployment {
	meta := &models.ServiceMetadata{
		Name:       "app",
		Networking: models.NetworkingConfig{Expose: true, Domain: "app.example.com", ContainerPort: 3000},
		Domains:    domains,
	}
	if sleep {
		meta.Sleep = &models.SleepConfig{Enabled: true, IdleMinutes: 15}
	}
	return &models.Deployment{Name: "app", Metadata: meta}
}

func TestRenderVirtualHost_SleepWake(t *testing.T) {
	m := NewManager(&config.NginxConfig{}, t.TempDir(), "")
	multi := []models.DomainConfig{{ID: "d1", Domain: "app.example.com", Service: "app", ContainerPort: 3000}}

	for _, domains := range [][]models.DomainConfig{nil, multi} {
		on, err := m.RenderVirtualHost(sleepDeployment(true, domains))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(on, "error_page 502 503 504 = @flatrun_wake;") ||
			!strings.Contains(on, "rewrite ^ /api/hooks/wake/app break;") {
			t.Errorf("sleep-enabled vhost (domains=%d) lacks the wake fallback:\n%s", len(domains), on)
		}
		off, err := m.RenderVirtualHost(sleepDeployment(false, domains))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(off, "flatrun_wake") {
			t.Errorf("vhost without sleep (domains=%d) should not have a wake fallback", len(domains))
		}
	}
}

// A location's own error_page replaces the server's, so a gated route needs the
// wake fallback repeated or a sleeping deployment shows nginx's 502.
func TestRenderVirtualHost_SleepWakeBehindAccessGate(t *testing.T) {
	m := NewManager(&config.NginxConfig{}, t.TempDir(), "")
	gate := &models.DomainAccessConfig{Enabled: true, Mode: "any_verified", EmailTargetID: "smtp"}
	domains := []models.DomainConfig{{ID: "d1", Domain: "app.example.com", Service: "app", ContainerPort: 3000, Access: gate}}

	on, err := m.RenderVirtualHost(sleepDeployment(true, domains))
	if err != nil {
		t.Fatal(err)
	}
	location := on[strings.Index(on, "auth_request /_flatrun/access/check;"):]
	location = location[:strings.Index(location, "proxy_pass")]
	if !strings.Contains(location, "error_page 502 503 504 = @flatrun_wake;") {
		t.Errorf("gated location lacks the wake fallback:\n%s", on)
	}
	off, err := m.RenderVirtualHost(sleepDeployment(false, domains))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(off, "flatrun_wake") {
		t.Errorf("gated vhost without sleep should not have a wake fallback:\n%s", off)
	}
}
