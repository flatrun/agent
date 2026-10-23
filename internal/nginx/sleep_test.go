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
