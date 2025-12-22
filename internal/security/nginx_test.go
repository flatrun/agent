package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureSecurityConfigFiles(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "security-nginx-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	confDir := filepath.Join(tmpDir, "nginx", "conf.d")

	generator := &NginxConfigGenerator{
		configPath: confDir,
	}

	t.Run("creates config files in non-existent directory", func(t *testing.T) {
		err := generator.EnsureSecurityConfigFiles()
		if err != nil {
			t.Fatalf("EnsureSecurityConfigFiles failed: %v", err)
		}

		rateLimitsPath := filepath.Join(confDir, "rate_limits.conf")
		if _, err := os.Stat(rateLimitsPath); os.IsNotExist(err) {
			t.Error("rate_limits.conf should be created")
		}
	})

	t.Run("rate_limits.conf has valid nginx content", func(t *testing.T) {
		rateLimitsPath := filepath.Join(confDir, "rate_limits.conf")
		content, err := os.ReadFile(rateLimitsPath)
		if err != nil {
			t.Fatalf("failed to read rate_limits.conf: %v", err)
		}

		if !strings.Contains(string(content), "# Auto-generated") {
			t.Error("rate_limits.conf should contain auto-generated comment")
		}
	})

	t.Run("does not overwrite existing files", func(t *testing.T) {
		rateLimitsPath := filepath.Join(confDir, "rate_limits.conf")
		customContent := "# Custom rate limits\nlimit_req_zone $binary_remote_addr zone=test:10m rate=1r/s;\n"
		if err := os.WriteFile(rateLimitsPath, []byte(customContent), 0644); err != nil {
			t.Fatalf("failed to write custom content: %v", err)
		}

		err := generator.EnsureSecurityConfigFiles()
		if err != nil {
			t.Fatalf("EnsureSecurityConfigFiles failed: %v", err)
		}

		content, err := os.ReadFile(rateLimitsPath)
		if err != nil {
			t.Fatalf("failed to read rate_limits.conf: %v", err)
		}

		if string(content) != customContent {
			t.Error("EnsureSecurityConfigFiles should not overwrite existing files")
		}
	})
}

func TestGenerateProtectedPathsConfig(t *testing.T) {
	generator := &NginxConfigGenerator{}

	t.Run("empty paths returns empty string", func(t *testing.T) {
		result := generator.GenerateProtectedPathsConfig(nil)
		if result != "" {
			t.Errorf("expected empty string for nil paths, got %q", result)
		}

		result = generator.GenerateProtectedPathsConfig([]string{})
		if result != "" {
			t.Errorf("expected empty string for empty paths, got %q", result)
		}
	})

	t.Run("generates location blocks for paths", func(t *testing.T) {
		paths := []string{".env", ".git"}
		result := generator.GenerateProtectedPathsConfig(paths)

		if !strings.Contains(result, "location ~") {
			t.Error("should contain location directives")
		}
		if !strings.Contains(result, "return 404") {
			t.Error("should return 404 for protected paths")
		}
	})
}

func TestGenerateZoneName(t *testing.T) {
	tests := []struct {
		pattern  string
		expected string
	}{
		{"/wp-login.php", "wp-login_php"},
		{"/admin", "admin"},
		{"/api/v1", "api_v1"},
		{"~.*\\.env", "env"},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			result := generateZoneName(tt.pattern)
			if result == "" {
				t.Errorf("generateZoneName(%q) returned empty string", tt.pattern)
			}
			if len(result) > 20 && !strings.HasPrefix(result, "zone_") {
				t.Errorf("long zone names should be hashed with zone_ prefix")
			}
		})
	}
}

func TestEscapeNginxRegex(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{".env", "\\.env"},
		{".git/*", "\\.git/.*"},
		{"wp-config.php", "wp-config\\.php"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := escapeNginxRegex(tt.input)
			if result != tt.expected {
				t.Errorf("escapeNginxRegex(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}
