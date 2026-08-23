package api

import (
	"net/http"
	"testing"
)

func TestSettingsUpdatePreservesOmittedCertbotFields(t *testing.T) {
	server, _, httpServer := setupPlanTestServer(t)
	server.config.Certbot.Enabled = true
	server.config.Certbot.Staging = true
	server.config.Certbot.Image = "certbot/certbot:latest"

	response, body := doJSON(t, http.MethodPut, httpServer.URL+"/api/settings", map[string]any{
		"certbot": map[string]any{"email": "ops@example.com"},
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %#v", response.StatusCode, body)
	}
	if !server.config.Certbot.Enabled || !server.config.Certbot.Staging {
		t.Fatalf("omitted booleans changed: enabled=%v staging=%v", server.config.Certbot.Enabled, server.config.Certbot.Staging)
	}
	if server.config.Certbot.Image != "certbot/certbot:latest" || server.config.Certbot.Email != "ops@example.com" {
		t.Fatalf("certbot settings = %#v", server.config.Certbot)
	}
}
