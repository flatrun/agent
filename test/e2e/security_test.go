package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	securityDeploymentsPath = "/tmp/flatrun-e2e-security"
	securityAPIPort         = "18091"
)

func TestSecurityConfigFilesCreated(t *testing.T) {
	if os.Getenv("FLATRUN_SECURITY_TEST") != "true" {
		t.Skip("Skipping security test - set FLATRUN_SECURITY_TEST=true to run")
	}

	startComposeEnv(t, "docker-compose.security.yml", securityDeploymentsPath, 120*time.Second)

	t.Run("rate_limits.conf exists", func(t *testing.T) {
		rateLimitsPath := filepath.Join(securityDeploymentsPath, "nginx", "conf.d", "rate_limits.conf")
		content, err := os.ReadFile(rateLimitsPath)
		if err != nil {
			t.Fatalf("rate_limits.conf should exist: %v", err)
		}
		if len(content) == 0 {
			t.Error("rate_limits.conf should not be empty")
		}
		t.Logf("rate_limits.conf content:\n%s", string(content))
	})

	t.Run("nginx is healthy with security configs", func(t *testing.T) {
		resp, err := http.Get("http://localhost:18081/health")
		if err != nil {
			t.Fatalf("nginx health check failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("nginx health check returned %d, expected 200", resp.StatusCode)
		}
	})

	t.Run("security stats endpoint works", func(t *testing.T) {
		resp, err := http.Get(fmt.Sprintf("http://localhost:%s/api/security/stats", securityAPIPort))
		if err != nil {
			t.Fatalf("security stats request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("security stats returned %d, expected 200", resp.StatusCode)
		}

		var stats map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
			t.Errorf("failed to decode security stats: %v", err)
		}
		t.Logf("Security stats: %+v", stats)
	})

	t.Run("blocked IPs endpoint works", func(t *testing.T) {
		resp, err := http.Get(fmt.Sprintf("http://localhost:%s/api/security/blocked-ips", securityAPIPort))
		if err != nil {
			t.Fatalf("blocked IPs request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("blocked IPs returned %d, expected 200", resp.StatusCode)
		}
	})

	t.Run("protected routes endpoint works", func(t *testing.T) {
		resp, err := http.Get(fmt.Sprintf("http://localhost:%s/api/security/protected-routes", securityAPIPort))
		if err != nil {
			t.Fatalf("protected routes request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("protected routes returned %d, expected 200", resp.StatusCode)
		}
	})
}
