package nginx

import (
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
)

func TestServiceSleepWakesOnlyThatServicesLocations(t *testing.T) {
	m := NewManager(&config.NginxConfig{}, t.TempDir(), "")
	api := site(func(d *models.DomainConfig) { d.ID = "api"; d.Service = "api"; d.PathPrefix = "/api" })
	deployment := &models.Deployment{Name: "shop", Metadata: &models.ServiceMetadata{
		Domains:      []models.DomainConfig{site(func(*models.DomainConfig) {}), api},
		ServiceSleep: map[string]models.SleepConfig{"api": {Enabled: true, IdleMinutes: 10}},
	}}
	out, err := m.renderMultiDomainConfig(deployment, false)
	if err != nil {
		t.Fatal(err)
	}

	apiLoc := locationBlock(t, out, "/api")
	for _, want := range []string{
		"error_page 502 503 504 = @flatrun_wake_service_0;",
		`set $flatrun_service "api";`,
	} {
		if !strings.Contains(apiLoc, want) {
			t.Errorf("api location is missing %s:\n%s", want, apiLoc)
		}
	}
	if !strings.Contains(out, "rewrite ^ /api/hooks/wake/shop/api break;") {
		t.Errorf("no wake location for api:\n%s", out)
	}
	if web := locationBlock(t, out, "/"); strings.Contains(web, "flatrun_wake") || !strings.Contains(web, `set $flatrun_service "web";`) {
		t.Errorf("web does not sleep and must not wake, but must still be attributed:\n%s", web)
	}

	deployment.Metadata.Sleep = &models.SleepConfig{Enabled: true, IdleMinutes: 15}
	out, err = m.renderMultiDomainConfig(deployment, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "flatrun_wake_service") {
		t.Errorf("while the whole deployment sleeps, only its own wake applies:\n%s", out)
	}
}
