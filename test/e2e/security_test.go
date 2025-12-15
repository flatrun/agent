package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
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

	// Setup security test environment
	if err := setupSecurityTestEnvironment(); err != nil {
		t.Fatalf("Failed to setup security test environment: %v", err)
	}
	defer cleanupSecurityTestEnvironment()

	// Wait for agent to be ready
	if err := waitForSecurityAgent(); err != nil {
		t.Fatalf("Security agent failed to start: %v", err)
	}

	// Test 1: Verify blocked_ips.conf exists and has valid content
	t.Run("blocked_ips.conf exists", func(t *testing.T) {
		blockedIPsPath := filepath.Join(securityDeploymentsPath, "nginx", "conf.d", "blocked_ips.conf")
		content, err := os.ReadFile(blockedIPsPath)
		if err != nil {
			t.Fatalf("blocked_ips.conf should exist: %v", err)
		}
		if len(content) == 0 {
			t.Error("blocked_ips.conf should not be empty")
		}
		t.Logf("blocked_ips.conf content:\n%s", string(content))
	})

	// Test 2: Verify rate_limits.conf exists and has valid content
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

	// Test 3: Verify nginx is healthy (means it could parse the config with includes)
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

	// Test 4: Verify security stats endpoint works
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

	// Test 5: Verify blocked IPs endpoint works
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

	// Test 6: Verify protected routes endpoint works
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

func setupSecurityTestEnvironment() error {
	// Clean and create directories
	_ = os.RemoveAll(securityDeploymentsPath)

	// Create nginx conf.d directory
	confDir := filepath.Join(securityDeploymentsPath, "nginx", "conf.d")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		return fmt.Errorf("failed to create conf.d directory: %w", err)
	}

	// Start security test environment
	cmd := exec.Command("docker", "compose", "-f", "docker-compose.security.yml", "up", "-d", "--build")
	cmd.Dir = getTestDir()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func cleanupSecurityTestEnvironment() {
	cmd := exec.Command("docker", "compose", "-f", "docker-compose.security.yml", "down", "-v", "--remove-orphans")
	cmd.Dir = getTestDir()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()

	_ = os.RemoveAll(securityDeploymentsPath)
}

func waitForSecurityAgent() error {
	deadline := time.Now().Add(120 * time.Second)

	for time.Now().Before(deadline) {
		resp, err := http.Get(fmt.Sprintf("http://localhost:%s/api/health", securityAPIPort))
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("timeout waiting for security agent")
}
