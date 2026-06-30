package infra

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/config"
)

func TestSetNginxRealtimeCapture(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "infra-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	nginxDir := filepath.Join(tmpDir, "nginx")
	confDir := filepath.Join(nginxDir, "conf.d")

	if err := os.MkdirAll(nginxDir, 0755); err != nil {
		t.Fatalf("failed to create nginx dir: %v", err)
	}

	// Create a minimal docker-compose.yml for nginx
	composeContent := `services:
  nginx:
    image: openresty/openresty:alpine
    container_name: flatrun-nginx
    volumes:
      - ./conf.d:/etc/nginx/conf.d
`
	composePath := filepath.Join(nginxDir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte(composeContent), 0644); err != nil {
		t.Fatalf("failed to write docker-compose.yml: %v", err)
	}

	cfg := &config.Config{
		DeploymentsPath: tmpDir,
		Nginx: config.NginxConfig{
			ConfigPath: confDir,
		},
	}

	m := NewManager(cfg)

	t.Run("enable realtime capture creates lua config and files", func(t *testing.T) {
		err := m.SetNginxRealtimeCapture(true)
		if err != nil {
			t.Fatalf("SetNginxRealtimeCapture(true) failed: %v", err)
		}

		nginxConfPath := filepath.Join(nginxDir, "nginx.conf")
		content, err := os.ReadFile(nginxConfPath)
		if err != nil {
			t.Fatalf("failed to read nginx.conf: %v", err)
		}

		if !strings.Contains(string(content), "lua_package_path") {
			t.Error("nginx.conf should contain lua_package_path when realtime capture is enabled")
		}
		if !strings.Contains(string(content), "init_by_lua_block") {
			t.Error("nginx.conf should contain init_by_lua_block when realtime capture is enabled")
		}

		luaPath := filepath.Join(nginxDir, "lua", "security.lua")
		if _, err := os.Stat(luaPath); os.IsNotExist(err) {
			t.Error("security.lua should be created when realtime capture is enabled")
		}

		rateLimitsPath := filepath.Join(confDir, "rate_limits.conf")
		if _, err := os.Stat(rateLimitsPath); os.IsNotExist(err) {
			t.Error("rate_limits.conf should be created")
		}
	})

	t.Run("disable realtime capture removes nginx.conf and lua directory", func(t *testing.T) {
		err := m.SetNginxRealtimeCapture(false)
		if err != nil {
			t.Fatalf("SetNginxRealtimeCapture(false) failed: %v", err)
		}

		// nginx.conf should be deleted - container will use default from image
		nginxConfPath := filepath.Join(nginxDir, "nginx.conf")
		if _, err := os.Stat(nginxConfPath); !os.IsNotExist(err) {
			t.Error("nginx.conf should be deleted when realtime capture is disabled")
		}

		// lua directory should be removed
		luaDir := filepath.Join(nginxDir, "lua")
		if _, err := os.Stat(luaDir); !os.IsNotExist(err) {
			t.Error("lua directory should be removed when realtime capture is disabled")
		}

		// conf.d files should still exist (they may be used for rate limiting etc)
		rateLimitsPath := filepath.Join(confDir, "rate_limits.conf")
		if _, err := os.Stat(rateLimitsPath); os.IsNotExist(err) {
			t.Error("rate_limits.conf should still exist after disabling realtime capture")
		}
	})

	t.Run("security volume mounts are added and removed correctly", func(t *testing.T) {
		// Enable to add volume mounts
		if err := m.SetNginxRealtimeCapture(true); err != nil {
			t.Fatalf("SetNginxRealtimeCapture(true) failed: %v", err)
		}

		composeContent, err := os.ReadFile(composePath)
		if err != nil {
			t.Fatalf("failed to read docker-compose.yml: %v", err)
		}

		if !strings.Contains(string(composeContent), "./nginx.conf:/usr/local/openresty/nginx/conf/nginx.conf:ro") {
			t.Error("compose should contain nginx.conf volume mount after enabling")
		}
		if !strings.Contains(string(composeContent), "./lua:/etc/nginx/lua:ro") {
			t.Error("compose should contain lua volume mount after enabling")
		}

		// Disable to remove volume mounts
		if err := m.SetNginxRealtimeCapture(false); err != nil {
			t.Fatalf("SetNginxRealtimeCapture(false) failed: %v", err)
		}

		composeContent, err = os.ReadFile(composePath)
		if err != nil {
			t.Fatalf("failed to read docker-compose.yml: %v", err)
		}

		if strings.Contains(string(composeContent), "./nginx.conf:/usr/local/openresty/nginx/conf/nginx.conf:ro") {
			t.Error("compose should NOT contain nginx.conf volume mount after disabling")
		}
		if strings.Contains(string(composeContent), "./lua:/etc/nginx/lua:ro") {
			t.Error("compose should NOT contain lua volume mount after disabling")
		}
	})

	t.Run("switching between configs preserves vhost configs", func(t *testing.T) {
		if err := os.MkdirAll(confDir, 0755); err != nil {
			t.Fatalf("failed to create conf.d: %v", err)
		}

		vhostContent := "server { listen 80; server_name test.example.com; }"
		vhostPath := filepath.Join(confDir, "test-app.conf")
		if err := os.WriteFile(vhostPath, []byte(vhostContent), 0644); err != nil {
			t.Fatalf("failed to write vhost config: %v", err)
		}

		if err := m.SetNginxRealtimeCapture(true); err != nil {
			t.Fatalf("SetNginxRealtimeCapture(true) failed: %v", err)
		}

		content, err := os.ReadFile(vhostPath)
		if err != nil {
			t.Fatalf("vhost config should still exist after enabling realtime capture: %v", err)
		}
		if string(content) != vhostContent {
			t.Error("vhost config content should be preserved")
		}

		if err := m.SetNginxRealtimeCapture(false); err != nil {
			t.Fatalf("SetNginxRealtimeCapture(false) failed: %v", err)
		}

		content, err = os.ReadFile(vhostPath)
		if err != nil {
			t.Fatalf("vhost config should still exist after disabling realtime capture: %v", err)
		}
		if string(content) != vhostContent {
			t.Error("vhost config content should be preserved")
		}
	})
}

func TestGetNginxDir(t *testing.T) {
	tests := []struct {
		name        string
		configPath  string
		deployments string
		expected    string
	}{
		{
			name:        "uses parent of config_path",
			configPath:  "/deployments/nginx/conf.d",
			deployments: "/deployments",
			expected:    "/deployments/nginx",
		},
		{
			name:        "falls back to deployments/nginx when config_path empty",
			configPath:  "",
			deployments: "/var/flatrun",
			expected:    "/var/flatrun/nginx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				DeploymentsPath: tt.deployments,
				Nginx: config.NginxConfig{
					ConfigPath: tt.configPath,
				},
			}
			m := NewManager(cfg)

			result := m.getNginxDir()
			if result != tt.expected {
				t.Errorf("getNginxDir() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// An upgraded agent must refresh an existing managed nginx.conf so it gains the
// server_names_hash sizing without a security toggle, and must not create one where
// none exists.
func TestEnsureBaseNginxConfig(t *testing.T) {
	t.Run("refreshes an existing managed config", func(t *testing.T) {
		nginxDir := t.TempDir()
		confPath := filepath.Join(nginxDir, "nginx.conf")
		// An older managed (lua) config that predates the server_names_hash settings.
		old := "http {\n    lua_package_path \"/etc/nginx/lua/?.lua;;\";\n    types_hash_max_size 2048;\n}\n"
		if err := os.WriteFile(confPath, []byte(old), 0644); err != nil {
			t.Fatal(err)
		}

		cfg := &config.Config{
			DeploymentsPath: nginxDir,
			Nginx:           config.NginxConfig{ConfigPath: filepath.Join(nginxDir, "conf.d")},
		}
		if err := NewManager(cfg).EnsureBaseNginxConfig(); err != nil {
			t.Fatalf("EnsureBaseNginxConfig() = %v", err)
		}

		content, err := os.ReadFile(confPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), "server_names_hash_bucket_size") {
			t.Errorf("refreshed config should contain server_names_hash_bucket_size, got:\n%s", content)
		}
	})

	t.Run("does not create a config where none exists", func(t *testing.T) {
		nginxDir := t.TempDir()
		cfg := &config.Config{
			DeploymentsPath: nginxDir,
			Nginx:           config.NginxConfig{ConfigPath: filepath.Join(nginxDir, "conf.d")},
		}
		if err := NewManager(cfg).EnsureBaseNginxConfig(); err != nil {
			t.Fatalf("EnsureBaseNginxConfig() = %v", err)
		}
		if _, err := os.Stat(filepath.Join(nginxDir, "nginx.conf")); !os.IsNotExist(err) {
			t.Errorf("EnsureBaseNginxConfig must not create a managed config where none existed")
		}
	})
}
