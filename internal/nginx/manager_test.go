package nginx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
)

func TestNewManager_ContainerWebrootPath(t *testing.T) {
	tests := []struct {
		name                     string
		containerWebrootPath     string
		expectedContainerWebroot string
	}{
		{
			name:                     "uses configured container webroot",
			containerWebrootPath:     "/custom/container/path",
			expectedContainerWebroot: "/custom/container/path",
		},
		{
			name:                     "uses default when not configured",
			containerWebrootPath:     "",
			expectedContainerWebroot: "/usr/share/nginx/html",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.NginxConfig{
				ContainerWebrootPath: tt.containerWebrootPath,
			}

			m := NewManager(cfg, "/deployments", "")

			if m.containerWebrootPath != tt.expectedContainerWebroot {
				t.Errorf("containerWebrootPath = %q, want %q", m.containerWebrootPath, tt.expectedContainerWebroot)
			}
		})
	}
}

func TestNewManager_WebrootPath(t *testing.T) {
	tests := []struct {
		name            string
		webrootPath     string
		deploymentsPath string
		expectedWebroot string
	}{
		{
			name:            "uses configured webroot path",
			webrootPath:     "/custom/webroot",
			deploymentsPath: "/deployments",
			expectedWebroot: "/custom/webroot",
		},
		{
			name:            "uses default when not configured",
			webrootPath:     "",
			deploymentsPath: "/deployments",
			expectedWebroot: "/deployments/nginx/html",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.NginxConfig{}

			m := NewManager(cfg, tt.deploymentsPath, tt.webrootPath)

			if m.webrootPath != tt.expectedWebroot {
				t.Errorf("webrootPath = %q, want %q", m.webrootPath, tt.expectedWebroot)
			}
		})
	}
}

func TestNewManager_ConfigPath(t *testing.T) {
	tests := []struct {
		name               string
		configPath         string
		deploymentsPath    string
		expectedConfigPath string
	}{
		{
			name:               "uses configured config path",
			configPath:         "/custom/conf.d",
			deploymentsPath:    "/deployments",
			expectedConfigPath: "/custom/conf.d",
		},
		{
			name:               "uses default when not configured",
			configPath:         "",
			deploymentsPath:    "/deployments",
			expectedConfigPath: "/deployments/nginx/conf.d",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.NginxConfig{
				ConfigPath: tt.configPath,
			}

			m := NewManager(cfg, tt.deploymentsPath, "")

			if m.configPath != tt.expectedConfigPath {
				t.Errorf("configPath = %q, want %q", m.configPath, tt.expectedConfigPath)
			}
		})
	}
}

func TestUpdateConfig_ContainerWebrootPath(t *testing.T) {
	cfg := &config.NginxConfig{
		ContainerWebrootPath: "/initial/container/webroot",
	}
	m := NewManager(cfg, "/deployments", "")

	if m.containerWebrootPath != "/initial/container/webroot" {
		t.Errorf("initial containerWebrootPath = %q, want %q", m.containerWebrootPath, "/initial/container/webroot")
	}

	newCfg := &config.NginxConfig{
		ContainerWebrootPath: "/updated/container/webroot",
	}
	m.UpdateConfig(newCfg, "/deployments", "")

	if m.containerWebrootPath != "/updated/container/webroot" {
		t.Errorf("updated containerWebrootPath = %q, want %q", m.containerWebrootPath, "/updated/container/webroot")
	}

	emptyCfg := &config.NginxConfig{
		ContainerWebrootPath: "",
	}
	m.UpdateConfig(emptyCfg, "/new/deployments", "")

	if m.containerWebrootPath != "/usr/share/nginx/html" {
		t.Errorf("default containerWebrootPath = %q, want %q", m.containerWebrootPath, "/usr/share/nginx/html")
	}
}

func TestUpdateConfig_WebrootPath(t *testing.T) {
	cfg := &config.NginxConfig{}
	m := NewManager(cfg, "/deployments", "/initial/webroot")

	if m.webrootPath != "/initial/webroot" {
		t.Errorf("initial webrootPath = %q, want %q", m.webrootPath, "/initial/webroot")
	}

	newCfg := &config.NginxConfig{}
	m.UpdateConfig(newCfg, "/deployments", "/updated/webroot")

	if m.webrootPath != "/updated/webroot" {
		t.Errorf("updated webrootPath = %q, want %q", m.webrootPath, "/updated/webroot")
	}

	m.UpdateConfig(newCfg, "/new/deployments", "")

	expected := filepath.Join("/new/deployments", "nginx", "html")
	if m.webrootPath != expected {
		t.Errorf("default webrootPath = %q, want %q", m.webrootPath, expected)
	}
}

func TestGenerateConfig_ContainerWebrootPath(t *testing.T) {
	tests := []struct {
		name                 string
		containerWebrootPath string
		sslEnabled           bool
	}{
		{
			name:                 "HTTP config uses container webroot",
			containerWebrootPath: "/custom/nginx/html",
			sslEnabled:           false,
		},
		{
			name:                 "SSL config uses container webroot",
			containerWebrootPath: "/custom/nginx/html",
			sslEnabled:           true,
		},
		{
			name:                 "default container webroot in HTTP",
			containerWebrootPath: "",
			sslEnabled:           false,
		},
		{
			name:                 "default container webroot in SSL",
			containerWebrootPath: "",
			sslEnabled:           true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.NginxConfig{
				ContainerWebrootPath: tt.containerWebrootPath,
			}

			m := NewManager(cfg, "/deployments", "/host/webroot")

			deployment := &models.Deployment{
				Name: "test-app",
				Metadata: &models.ServiceMetadata{
					Networking: models.NetworkingConfig{
						Expose:        true,
						Domain:        "test.example.com",
						ContainerPort: 8080,
					},
					SSL: models.SSLConfig{
						Enabled: tt.sslEnabled,
					},
				},
			}

			configContent, err := m.generateConfig(deployment)
			if err != nil {
				t.Fatalf("generateConfig failed: %v", err)
			}

			expectedPath := tt.containerWebrootPath
			if expectedPath == "" {
				expectedPath = "/usr/share/nginx/html"
			}

			expectedLine := "root " + expectedPath + ";"
			if !strings.Contains(configContent, expectedLine) {
				t.Errorf("config does not contain expected webroot path %q\nConfig:\n%s", expectedLine, configContent)
			}

			hostWebrootLine := "root /host/webroot;"
			if strings.Contains(configContent, hostWebrootLine) {
				t.Errorf("config should not contain host webroot path %q", hostWebrootLine)
			}
		})
	}
}

func TestCreateVirtualHost(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "nginx-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.NginxConfig{
		ContainerWebrootPath: "/var/www/html",
	}

	m := NewManager(cfg, tmpDir, "")

	deployment := &models.Deployment{
		Name: "test-deployment",
		Metadata: &models.ServiceMetadata{
			Networking: models.NetworkingConfig{
				Expose:        true,
				Domain:        "test.example.com",
				ContainerPort: 3000,
			},
			SSL: models.SSLConfig{
				Enabled: false,
			},
		},
	}

	err = m.CreateVirtualHost(deployment)
	if err != nil {
		t.Fatalf("CreateVirtualHost failed: %v", err)
	}

	configFile := filepath.Join(tmpDir, "nginx", "conf.d", "test-deployment.conf")
	content, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	if !strings.Contains(string(content), "server_name test.example.com") {
		t.Error("config does not contain expected server_name")
	}

	if !strings.Contains(string(content), "root /var/www/html;") {
		t.Error("config does not contain expected container webroot path")
	}
}

func TestCreateVirtualHost_NotExposed(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "nginx-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.NginxConfig{}
	m := NewManager(cfg, tmpDir, "")

	deployment := &models.Deployment{
		Name: "test-deployment",
		Metadata: &models.ServiceMetadata{
			Networking: models.NetworkingConfig{
				Expose: false,
			},
		},
	}

	err = m.CreateVirtualHost(deployment)
	if err != nil {
		t.Fatalf("CreateVirtualHost should not fail for non-exposed: %v", err)
	}

	configFile := filepath.Join(tmpDir, "nginx", "conf.d", "test-deployment.conf")
	if _, err := os.Stat(configFile); !os.IsNotExist(err) {
		t.Error("config file should not be created for non-exposed deployment")
	}
}

func TestDeleteVirtualHost(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "nginx-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	confDir := filepath.Join(tmpDir, "nginx", "conf.d")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		t.Fatalf("failed to create conf.d: %v", err)
	}

	configFile := filepath.Join(confDir, "test-app.conf")
	if err := os.WriteFile(configFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test config: %v", err)
	}

	cfg := &config.NginxConfig{}
	m := NewManager(cfg, tmpDir, "")

	err = m.DeleteVirtualHost("test-app")
	if err != nil {
		t.Fatalf("DeleteVirtualHost failed: %v", err)
	}

	if _, err := os.Stat(configFile); !os.IsNotExist(err) {
		t.Error("config file should be deleted")
	}
}

func TestVirtualHostExists(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "nginx-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	confDir := filepath.Join(tmpDir, "nginx", "conf.d")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		t.Fatalf("failed to create conf.d: %v", err)
	}

	configFile := filepath.Join(confDir, "existing-app.conf")
	if err := os.WriteFile(configFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test config: %v", err)
	}

	cfg := &config.NginxConfig{}
	m := NewManager(cfg, tmpDir, "")

	if !m.VirtualHostExists("existing-app") {
		t.Error("VirtualHostExists should return true for existing config")
	}

	if m.VirtualHostExists("non-existing-app") {
		t.Error("VirtualHostExists should return false for non-existing config")
	}
}

func TestGetVhostsUsingSSLDomain(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "nginx-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	confDir := filepath.Join(tmpDir, "nginx", "conf.d")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		t.Fatalf("failed to create conf.d: %v", err)
	}

	sslConfig := `server {
    listen 443 ssl;
    server_name example.com;
    ssl_certificate /etc/letsencrypt/live/example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/example.com/privkey.pem;
}`
	if err := os.WriteFile(filepath.Join(confDir, "ssl-app.conf"), []byte(sslConfig), 0644); err != nil {
		t.Fatalf("failed to create ssl config: %v", err)
	}

	httpConfig := `server {
    listen 80;
    server_name other.com;
}`
	if err := os.WriteFile(filepath.Join(confDir, "http-app.conf"), []byte(httpConfig), 0644); err != nil {
		t.Fatalf("failed to create http config: %v", err)
	}

	cfg := &config.NginxConfig{}
	m := NewManager(cfg, tmpDir, "")

	vhosts := m.GetVhostsUsingSSLDomain("example.com")
	if len(vhosts) != 1 {
		t.Errorf("expected 1 vhost using example.com SSL, got %d", len(vhosts))
	}
	if len(vhosts) > 0 && vhosts[0] != "ssl-app" {
		t.Errorf("expected vhost 'ssl-app', got %q", vhosts[0])
	}

	vhosts = m.GetVhostsUsingSSLDomain("other.com")
	if len(vhosts) != 0 {
		t.Errorf("expected 0 vhosts using other.com SSL, got %d", len(vhosts))
	}

	vhosts = m.GetVhostsUsingSSLDomain("nonexistent.com")
	if len(vhosts) != 0 {
		t.Errorf("expected 0 vhosts for nonexistent domain, got %d", len(vhosts))
	}
}

func TestGetVhostsUsingSSLDomain_MultipleVhosts(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "nginx-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	confDir := filepath.Join(tmpDir, "nginx", "conf.d")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		t.Fatalf("failed to create conf.d: %v", err)
	}

	sslConfig1 := `server {
    listen 443 ssl;
    server_name app1.example.com;
    ssl_certificate /etc/letsencrypt/live/wildcard.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/wildcard.example.com/privkey.pem;
}`
	if err := os.WriteFile(filepath.Join(confDir, "app1.conf"), []byte(sslConfig1), 0644); err != nil {
		t.Fatalf("failed to create config: %v", err)
	}

	sslConfig2 := `server {
    listen 443 ssl;
    server_name app2.example.com;
    ssl_certificate /etc/letsencrypt/live/wildcard.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/wildcard.example.com/privkey.pem;
}`
	if err := os.WriteFile(filepath.Join(confDir, "app2.conf"), []byte(sslConfig2), 0644); err != nil {
		t.Fatalf("failed to create config: %v", err)
	}

	cfg := &config.NginxConfig{}
	m := NewManager(cfg, tmpDir, "")

	vhosts := m.GetVhostsUsingSSLDomain("wildcard.example.com")
	if len(vhosts) != 2 {
		t.Errorf("expected 2 vhosts using wildcard cert, got %d", len(vhosts))
	}
}

func TestGetVhostsUsingSSLDomain_EmptyDirectory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "nginx-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	confDir := filepath.Join(tmpDir, "nginx", "conf.d")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		t.Fatalf("failed to create conf.d: %v", err)
	}

	cfg := &config.NginxConfig{}
	m := NewManager(cfg, tmpDir, "")

	vhosts := m.GetVhostsUsingSSLDomain("example.com")
	if len(vhosts) != 0 {
		t.Errorf("expected 0 vhosts for empty directory, got %d", len(vhosts))
	}
}

func TestGetVhostsUsingSSLDomain_NonExistentDirectory(t *testing.T) {
	cfg := &config.NginxConfig{}
	m := NewManager(cfg, "/nonexistent/path", "")

	vhosts := m.GetVhostsUsingSSLDomain("example.com")
	if len(vhosts) != 0 {
		t.Errorf("expected 0 vhosts for nonexistent directory, got %d", len(vhosts))
	}
}

func TestGenerateConfig_HealthPathRootNoDuplicate(t *testing.T) {
	cfg := &config.NginxConfig{
		ContainerWebrootPath: "/var/www/html",
	}
	m := NewManager(cfg, "/deployments", "")

	tests := []struct {
		name       string
		healthPath string
		sslEnabled bool
	}{
		{
			name:       "HTTP with health path /",
			healthPath: "/",
			sslEnabled: false,
		},
		{
			name:       "SSL with health path /",
			healthPath: "/",
			sslEnabled: true,
		},
		{
			name:       "HTTP with health path /health",
			healthPath: "/health",
			sslEnabled: false,
		},
		{
			name:       "SSL with health path /health",
			healthPath: "/health",
			sslEnabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deployment := &models.Deployment{
				Name: "test-app",
				Metadata: &models.ServiceMetadata{
					Networking: models.NetworkingConfig{
						Expose:        true,
						Domain:        "test.example.com",
						ContainerPort: 8080,
					},
					SSL: models.SSLConfig{
						Enabled: tt.sslEnabled,
					},
					HealthCheck: models.HealthCheckConfig{
						Path: tt.healthPath,
					},
				},
			}

			configContent, err := m.generateConfig(deployment)
			if err != nil {
				t.Fatalf("generateConfig failed: %v", err)
			}

			locationCount := strings.Count(configContent, "location / {")

			if tt.sslEnabled {
				if locationCount != 2 {
					t.Errorf("SSL config should have exactly 2 'location / {' blocks (port 80 redirect + port 443 proxy), got %d\nConfig:\n%s", locationCount, configContent)
				}
			} else {
				if locationCount != 1 {
					t.Errorf("HTTP config should have exactly 1 'location / {' block, got %d\nConfig:\n%s", locationCount, configContent)
				}
			}

			if tt.healthPath != "/" {
				expectedHealthLocation := "location " + tt.healthPath + " {"
				if !strings.Contains(configContent, expectedHealthLocation) {
					t.Errorf("config should contain health location %q\nConfig:\n%s", expectedHealthLocation, configContent)
				}
			}
		})
	}
}

func TestOpenRestyCompatibility(t *testing.T) {
	t.Run("Generated config is OpenResty compatible", func(t *testing.T) {
		cfg := &config.NginxConfig{
			ContainerWebrootPath: "/usr/share/nginx/html",
		}
		m := NewManager(cfg, "/deployments", "")

		deployment := &models.Deployment{
			Name: "openresty-test",
			Metadata: &models.ServiceMetadata{
				Networking: models.NetworkingConfig{
					Expose:        true,
					Domain:        "openresty.example.com",
					ContainerPort: 8080,
				},
				SSL: models.SSLConfig{
					Enabled: false,
				},
			},
		}

		configContent, err := m.generateConfig(deployment)
		if err != nil {
			t.Fatalf("generateConfig failed: %v", err)
		}

		// OpenResty requires standard nginx directives - verify key ones
		requiredDirectives := []string{
			"server {",
			"listen 80;",
			"server_name openresty.example.com;",
			"location",
			"proxy_pass",
			"proxy_http_version 1.1;",
			"proxy_set_header",
		}

		for _, directive := range requiredDirectives {
			if !strings.Contains(configContent, directive) {
				t.Errorf("Config missing required directive: %s", directive)
			}
		}

		// Verify no nginx-specific syntax that OpenResty doesn't support
		incompatiblePatterns := []string{
			"ngx_http_v2_module", // Only if not installed
		}

		for _, pattern := range incompatiblePatterns {
			if strings.Contains(configContent, pattern) {
				t.Errorf("Config contains potentially incompatible pattern: %s", pattern)
			}
		}
	})

	t.Run("SSL config is OpenResty compatible", func(t *testing.T) {
		cfg := &config.NginxConfig{
			ContainerWebrootPath: "/usr/share/nginx/html",
		}
		m := NewManager(cfg, "/deployments", "")

		deployment := &models.Deployment{
			Name: "openresty-ssl-test",
			Metadata: &models.ServiceMetadata{
				Networking: models.NetworkingConfig{
					Expose:        true,
					Domain:        "secure.example.com",
					ContainerPort: 8080,
				},
				SSL: models.SSLConfig{
					Enabled: true,
				},
			},
		}

		configContent, err := m.generateConfig(deployment)
		if err != nil {
			t.Fatalf("generateConfig failed: %v", err)
		}

		// OpenResty SSL directives
		sslDirectives := []string{
			"listen 443 ssl;",
			"ssl_certificate",
			"ssl_certificate_key",
			"ssl_protocols TLSv1.2 TLSv1.3;",
			"ssl_prefer_server_ciphers",
		}

		for _, directive := range sslDirectives {
			if !strings.Contains(configContent, directive) {
				t.Errorf("SSL config missing directive: %s", directive)
			}
		}
	})
}

func TestConfigImageCompatibility(t *testing.T) {
	tests := []struct {
		name       string
		image      string
		shouldWork bool
	}{
		{
			name:       "nginx:alpine works",
			image:      "nginx:alpine",
			shouldWork: true,
		},
		{
			name:       "openresty/openresty:alpine works",
			image:      "openresty/openresty:alpine",
			shouldWork: true,
		},
		{
			name:       "nginx:latest works",
			image:      "nginx:latest",
			shouldWork: true,
		},
		{
			name:       "openresty/openresty:latest works",
			image:      "openresty/openresty:latest",
			shouldWork: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.NginxConfig{
				Image:                tt.image,
				ContainerWebrootPath: "/usr/share/nginx/html",
			}

			m := NewManager(cfg, "/deployments", "")

			deployment := &models.Deployment{
				Name: "image-compat-test",
				Metadata: &models.ServiceMetadata{
					Networking: models.NetworkingConfig{
						Expose:        true,
						Domain:        "test.example.com",
						ContainerPort: 3000,
					},
				},
			}

			configContent, err := m.generateConfig(deployment)
			if tt.shouldWork {
				if err != nil {
					t.Errorf("Expected config generation to succeed for %s, got error: %v", tt.image, err)
				}
				if configContent == "" {
					t.Errorf("Expected non-empty config for %s", tt.image)
				}
			}
		})
	}
}

func TestUpdateVirtualHost_PortChange(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "nginx-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.NginxConfig{
		ContainerWebrootPath: "/var/www/html",
	}
	m := NewManager(cfg, tmpDir, "")

	deployment := &models.Deployment{
		Name: "test-deployment",
		Metadata: &models.ServiceMetadata{
			Networking: models.NetworkingConfig{
				Expose:        true,
				Domain:        "test.example.com",
				ContainerPort: 3000,
			},
			SSL: models.SSLConfig{
				Enabled: false,
			},
		},
	}

	err = m.CreateVirtualHost(deployment)
	if err != nil {
		t.Fatalf("CreateVirtualHost failed: %v", err)
	}

	configFile := filepath.Join(tmpDir, "nginx", "conf.d", "test-deployment.conf")
	content, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	if !strings.Contains(string(content), "test-deployment:3000") {
		t.Error("initial config should contain port 3000")
	}

	deployment.Metadata.Networking.ContainerPort = 8080
	err = m.UpdateVirtualHost(deployment)
	if err != nil {
		t.Fatalf("UpdateVirtualHost failed: %v", err)
	}

	content, err = os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("failed to read updated config file: %v", err)
	}

	if !strings.Contains(string(content), "test-deployment:8080") {
		t.Error("updated config should contain port 8080")
	}

	if strings.Contains(string(content), "test-deployment:3000") {
		t.Error("updated config should not contain old port 3000")
	}
}

func TestGenerateConfig_SecurityEnabled(t *testing.T) {
	cfg := &config.NginxConfig{
		ContainerWebrootPath: "/var/www/html",
	}
	m := NewManager(cfg, "/deployments", "")

	tests := []struct {
		name            string
		securityEnabled bool
		sslEnabled      bool
	}{
		{
			name:            "HTTP without security",
			securityEnabled: false,
			sslEnabled:      false,
		},
		{
			name:            "HTTP with security",
			securityEnabled: true,
			sslEnabled:      false,
		},
		{
			name:            "SSL without security",
			securityEnabled: false,
			sslEnabled:      true,
		},
		{
			name:            "SSL with security",
			securityEnabled: true,
			sslEnabled:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var security *models.DeploymentSecurityConfig
			if tt.securityEnabled {
				security = &models.DeploymentSecurityConfig{
					Enabled: true,
				}
			}

			deployment := &models.Deployment{
				Name: "security-test",
				Metadata: &models.ServiceMetadata{
					Networking: models.NetworkingConfig{
						Expose:        true,
						Domain:        "secure.example.com",
						ContainerPort: 8080,
					},
					SSL: models.SSLConfig{
						Enabled: tt.sslEnabled,
					},
					Security: security,
				},
			}

			configContent, err := m.generateConfig(deployment)
			if err != nil {
				t.Fatalf("generateConfig failed: %v", err)
			}

			luaBlockPresent := strings.Contains(configContent, "log_by_lua_block")
			securityCapturePresent := strings.Contains(configContent, "security.capture_event()")

			if tt.securityEnabled {
				if !luaBlockPresent {
					t.Errorf("Config should contain log_by_lua_block when security is enabled\nConfig:\n%s", configContent)
				}
				if !securityCapturePresent {
					t.Errorf("Config should contain security.capture_event() when security is enabled\nConfig:\n%s", configContent)
				}
			} else {
				if luaBlockPresent {
					t.Errorf("Config should NOT contain log_by_lua_block when security is disabled\nConfig:\n%s", configContent)
				}
				if securityCapturePresent {
					t.Errorf("Config should NOT contain security.capture_event() when security is disabled\nConfig:\n%s", configContent)
				}
			}

			// Verify config remains structurally valid
			if !strings.Contains(configContent, "server {") {
				t.Error("Config missing server block")
			}
			if !strings.Contains(configContent, "location / {") {
				t.Error("Config missing main location block")
			}
			if !strings.Contains(configContent, "proxy_pass") {
				t.Error("Config missing proxy_pass directive")
			}
		})
	}
}

func TestGenerateConfig_PerDeploymentBlockedIPs(t *testing.T) {
	cfg := &config.NginxConfig{
		ContainerWebrootPath: "/var/www/html",
	}
	m := NewManager(cfg, "/deployments", "")

	tests := []struct {
		name       string
		blockedIPs []string
		sslEnabled bool
	}{
		{
			name:       "HTTP with no blocked IPs",
			blockedIPs: nil,
			sslEnabled: false,
		},
		{
			name:       "HTTP with single blocked IP",
			blockedIPs: []string{"192.168.1.100"},
			sslEnabled: false,
		},
		{
			name:       "HTTP with multiple blocked IPs",
			blockedIPs: []string{"192.168.1.100", "10.0.0.50", "172.16.0.1"},
			sslEnabled: false,
		},
		{
			name:       "SSL with blocked IPs",
			blockedIPs: []string{"192.168.1.100", "10.0.0.50"},
			sslEnabled: true,
		},
		{
			name:       "HTTP with CIDR blocked",
			blockedIPs: []string{"192.168.1.0/24"},
			sslEnabled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deployment := &models.Deployment{
				Name: "blocked-ip-test",
				Metadata: &models.ServiceMetadata{
					Networking: models.NetworkingConfig{
						Expose:        true,
						Domain:        "blocked.example.com",
						ContainerPort: 8080,
					},
					SSL: models.SSLConfig{
						Enabled: tt.sslEnabled,
					},
					Security: &models.DeploymentSecurityConfig{
						Enabled:    true,
						BlockedIPs: tt.blockedIPs,
					},
				},
			}

			configContent, err := m.generateConfig(deployment)
			if err != nil {
				t.Fatalf("generateConfig failed: %v", err)
			}

			for _, ip := range tt.blockedIPs {
				expectedDeny := "deny " + ip + ";"
				if !strings.Contains(configContent, expectedDeny) {
					t.Errorf("Config should contain '%s'\nConfig:\n%s", expectedDeny, configContent)
				}
			}

			denyCount := strings.Count(configContent, "deny ")
			if denyCount != len(tt.blockedIPs) {
				t.Errorf("Expected %d deny directives, got %d\nConfig:\n%s", len(tt.blockedIPs), denyCount, configContent)
			}

			// Verify config structure remains valid
			if !strings.Contains(configContent, "server {") {
				t.Error("Config missing server block")
			}
			if !strings.Contains(configContent, "location / {") {
				t.Error("Config missing main location block")
			}
		})
	}
}

func TestGenerateConfig_PerDeploymentRateLimits(t *testing.T) {
	cfg := &config.NginxConfig{
		ContainerWebrootPath: "/var/www/html",
	}
	m := NewManager(cfg, "/deployments", "")

	tests := []struct {
		name       string
		rateLimits []models.DeploymentRateLimit
		sslEnabled bool
	}{
		{
			name:       "HTTP with no rate limits",
			rateLimits: nil,
			sslEnabled: false,
		},
		{
			name: "HTTP with single rate limit",
			rateLimits: []models.DeploymentRateLimit{
				{Path: "/api", Rate: 60, Burst: 10, Enabled: true},
			},
			sslEnabled: false,
		},
		{
			name: "HTTP with multiple rate limits",
			rateLimits: []models.DeploymentRateLimit{
				{Path: "/api", Rate: 60, Burst: 10, Enabled: true},
				{Path: "/login", Rate: 10, Burst: 5, Enabled: true},
			},
			sslEnabled: false,
		},
		{
			name: "SSL with rate limits",
			rateLimits: []models.DeploymentRateLimit{
				{Path: "/api", Rate: 100, Burst: 20, Enabled: true},
			},
			sslEnabled: true,
		},
		{
			name: "Mixed enabled and disabled rate limits",
			rateLimits: []models.DeploymentRateLimit{
				{Path: "/api", Rate: 60, Burst: 10, Enabled: true},
				{Path: "/public", Rate: 1000, Burst: 100, Enabled: false},
			},
			sslEnabled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deployment := &models.Deployment{
				Name: "rate-limit-test",
				Metadata: &models.ServiceMetadata{
					Networking: models.NetworkingConfig{
						Expose:        true,
						Domain:        "ratelimit.example.com",
						ContainerPort: 8080,
					},
					SSL: models.SSLConfig{
						Enabled: tt.sslEnabled,
					},
					Security: &models.DeploymentSecurityConfig{
						Enabled:    true,
						RateLimits: tt.rateLimits,
					},
				},
			}

			configContent, err := m.generateConfig(deployment)
			if err != nil {
				t.Fatalf("generateConfig failed: %v", err)
			}

			enabledCount := 0
			for _, rl := range tt.rateLimits {
				if rl.Enabled {
					enabledCount++
					expectedLocation := "location " + rl.Path + " {"
					if !strings.Contains(configContent, expectedLocation) {
						t.Errorf("Config should contain location block for '%s'\nConfig:\n%s", rl.Path, configContent)
					}
					if !strings.Contains(configContent, "limit_req zone=") {
						t.Errorf("Config should contain limit_req directive\nConfig:\n%s", configContent)
					}
					if !strings.Contains(configContent, "limit_req_status 429;") {
						t.Errorf("Config should contain limit_req_status 429\nConfig:\n%s", configContent)
					}
				} else {
					// Disabled rate limits should not appear
					expectedLocation := "location " + rl.Path + " {"
					if strings.Contains(configContent, expectedLocation) {
						t.Errorf("Config should NOT contain location block for disabled rate limit '%s'\nConfig:\n%s", rl.Path, configContent)
					}
				}
			}

			// Verify config structure remains valid
			if !strings.Contains(configContent, "server {") {
				t.Error("Config missing server block")
			}
			if !strings.Contains(configContent, "location / {") {
				t.Error("Config missing main location block")
			}
		})
	}
}

func TestGenerateConfig_SecurityWithBlockedIPsAndRateLimits(t *testing.T) {
	cfg := &config.NginxConfig{
		ContainerWebrootPath: "/var/www/html",
	}
	m := NewManager(cfg, "/deployments", "")

	deployment := &models.Deployment{
		Name: "full-security-test",
		Metadata: &models.ServiceMetadata{
			Networking: models.NetworkingConfig{
				Expose:        true,
				Domain:        "fullsecurity.example.com",
				ContainerPort: 8080,
			},
			SSL: models.SSLConfig{
				Enabled: true,
			},
			Security: &models.DeploymentSecurityConfig{
				Enabled:    true,
				BlockedIPs: []string{"192.168.1.100", "10.0.0.50"},
				RateLimits: []models.DeploymentRateLimit{
					{Path: "/api", Rate: 60, Burst: 10, Enabled: true},
					{Path: "/login", Rate: 10, Burst: 5, Enabled: true},
				},
			},
		},
	}

	configContent, err := m.generateConfig(deployment)
	if err != nil {
		t.Fatalf("generateConfig failed: %v", err)
	}

	// Verify Lua security hook
	if !strings.Contains(configContent, "log_by_lua_block") {
		t.Error("Config should contain log_by_lua_block")
	}
	if !strings.Contains(configContent, "security.capture_event()") {
		t.Error("Config should contain security.capture_event()")
	}

	// Verify blocked IPs
	if !strings.Contains(configContent, "deny 192.168.1.100;") {
		t.Error("Config should contain deny for 192.168.1.100")
	}
	if !strings.Contains(configContent, "deny 10.0.0.50;") {
		t.Error("Config should contain deny for 10.0.0.50")
	}

	// Verify rate limit locations
	if !strings.Contains(configContent, "location /api {") {
		t.Error("Config should contain /api rate limit location")
	}
	if !strings.Contains(configContent, "location /login {") {
		t.Error("Config should contain /login rate limit location")
	}
	if !strings.Contains(configContent, "limit_req zone=") {
		t.Error("Config should contain limit_req directive")
	}

	// Verify SSL directives
	if !strings.Contains(configContent, "listen 443 ssl;") {
		t.Error("Config should contain SSL listen directive")
	}
	if !strings.Contains(configContent, "ssl_certificate") {
		t.Error("Config should contain ssl_certificate")
	}

	// Count server blocks (should be 2 for SSL: port 80 redirect + port 443)
	serverCount := strings.Count(configContent, "server {")
	if serverCount != 2 {
		t.Errorf("SSL config should have 2 server blocks, got %d\nConfig:\n%s", serverCount, configContent)
	}
}

func TestGenerateConfig_DisabledSecurityNoLuaDirectives(t *testing.T) {
	cfg := &config.NginxConfig{
		ContainerWebrootPath: "/var/www/html",
	}
	m := NewManager(cfg, "/deployments", "")

	deployment := &models.Deployment{
		Name: "disabled-security-test",
		Metadata: &models.ServiceMetadata{
			Networking: models.NetworkingConfig{
				Expose:        true,
				Domain:        "nosecurity.example.com",
				ContainerPort: 8080,
			},
			SSL: models.SSLConfig{
				Enabled: false,
			},
			Security: &models.DeploymentSecurityConfig{
				Enabled:    false,
				BlockedIPs: []string{"192.168.1.100"},
				RateLimits: []models.DeploymentRateLimit{
					{Path: "/api", Rate: 60, Burst: 10, Enabled: true},
				},
			},
		},
	}

	configContent, err := m.generateConfig(deployment)
	if err != nil {
		t.Fatalf("generateConfig failed: %v", err)
	}

	// When security is disabled, no security features should be present
	if strings.Contains(configContent, "log_by_lua_block") {
		t.Errorf("Config should NOT contain log_by_lua_block when security is disabled\nConfig:\n%s", configContent)
	}
	if strings.Contains(configContent, "security.capture_event()") {
		t.Errorf("Config should NOT contain security.capture_event() when security is disabled\nConfig:\n%s", configContent)
	}
	if strings.Contains(configContent, "deny 192.168.1.100;") {
		t.Errorf("Config should NOT contain deny directives when security is disabled\nConfig:\n%s", configContent)
	}
	if strings.Contains(configContent, "limit_req zone=") {
		t.Errorf("Config should NOT contain limit_req when security is disabled\nConfig:\n%s", configContent)
	}

	// Basic structure should still be valid
	if !strings.Contains(configContent, "server {") {
		t.Error("Config missing server block")
	}
	if !strings.Contains(configContent, "location / {") {
		t.Error("Config missing main location block")
	}
	if !strings.Contains(configContent, "proxy_pass") {
		t.Error("Config missing proxy_pass directive")
	}
}

func TestGenerateConfig_NilSecurityConfig(t *testing.T) {
	cfg := &config.NginxConfig{
		ContainerWebrootPath: "/var/www/html",
	}
	m := NewManager(cfg, "/deployments", "")

	deployment := &models.Deployment{
		Name: "nil-security-test",
		Metadata: &models.ServiceMetadata{
			Networking: models.NetworkingConfig{
				Expose:        true,
				Domain:        "nilsecurity.example.com",
				ContainerPort: 8080,
			},
			SSL: models.SSLConfig{
				Enabled: false,
			},
			Security: nil,
		},
	}

	configContent, err := m.generateConfig(deployment)
	if err != nil {
		t.Fatalf("generateConfig failed: %v", err)
	}

	// No security features should be present
	if strings.Contains(configContent, "log_by_lua_block") {
		t.Errorf("Config should NOT contain log_by_lua_block when security is nil\nConfig:\n%s", configContent)
	}
	if strings.Contains(configContent, "deny ") {
		t.Errorf("Config should NOT contain deny directives when security is nil\nConfig:\n%s", configContent)
	}
	if strings.Contains(configContent, "limit_req") {
		t.Errorf("Config should NOT contain limit_req when security is nil\nConfig:\n%s", configContent)
	}

	// Basic structure should be valid
	if !strings.Contains(configContent, "server {") {
		t.Error("Config missing server block")
	}
	if !strings.Contains(configContent, "proxy_pass") {
		t.Error("Config missing proxy_pass directive")
	}
}

func TestCreateVirtualHost_WithSecurity(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "nginx-security-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.NginxConfig{
		ContainerWebrootPath: "/var/www/html",
	}
	m := NewManager(cfg, tmpDir, "")

	deployment := &models.Deployment{
		Name: "security-deployment",
		Metadata: &models.ServiceMetadata{
			Networking: models.NetworkingConfig{
				Expose:        true,
				Domain:        "security.example.com",
				ContainerPort: 3000,
			},
			SSL: models.SSLConfig{
				Enabled: false,
			},
			Security: &models.DeploymentSecurityConfig{
				Enabled:    true,
				BlockedIPs: []string{"192.168.1.100"},
				RateLimits: []models.DeploymentRateLimit{
					{Path: "/api", Rate: 60, Burst: 10, Enabled: true},
				},
			},
		},
	}

	err = m.CreateVirtualHost(deployment)
	if err != nil {
		t.Fatalf("CreateVirtualHost failed: %v", err)
	}

	configFile := filepath.Join(tmpDir, "nginx", "conf.d", "security-deployment.conf")
	content, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	// Verify security features in written file
	if !strings.Contains(string(content), "log_by_lua_block") {
		t.Error("Written config should contain log_by_lua_block")
	}
	if !strings.Contains(string(content), "deny 192.168.1.100;") {
		t.Error("Written config should contain deny directive")
	}
	if !strings.Contains(string(content), "limit_req zone=") {
		t.Error("Written config should contain limit_req directive")
	}

	// Verify rate_limits.conf was created
	rateLimitsFile := filepath.Join(tmpDir, "nginx", "conf.d", "rate_limits.conf")
	rateLimitsContent, err := os.ReadFile(rateLimitsFile)
	if err != nil {
		t.Fatalf("failed to read rate_limits.conf: %v", err)
	}
	if !strings.Contains(string(rateLimitsContent), "security-deployment") {
		t.Error("rate_limits.conf should contain deployment name")
	}
	if !strings.Contains(string(rateLimitsContent), "limit_req_zone") {
		t.Error("rate_limits.conf should contain limit_req_zone")
	}
}

func TestUpdateVirtualHost_ToggleSecurity(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "nginx-toggle-security-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.NginxConfig{
		ContainerWebrootPath: "/var/www/html",
	}
	m := NewManager(cfg, tmpDir, "")

	// Create without security
	deployment := &models.Deployment{
		Name: "toggle-security",
		Metadata: &models.ServiceMetadata{
			Networking: models.NetworkingConfig{
				Expose:        true,
				Domain:        "toggle.example.com",
				ContainerPort: 3000,
			},
			SSL: models.SSLConfig{
				Enabled: false,
			},
			Security: nil,
		},
	}

	err = m.CreateVirtualHost(deployment)
	if err != nil {
		t.Fatalf("CreateVirtualHost failed: %v", err)
	}

	configFile := filepath.Join(tmpDir, "nginx", "conf.d", "toggle-security.conf")
	content, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	if strings.Contains(string(content), "log_by_lua_block") {
		t.Error("Initial config should NOT contain log_by_lua_block")
	}

	// Enable security
	deployment.Metadata.Security = &models.DeploymentSecurityConfig{
		Enabled:    true,
		BlockedIPs: []string{"10.0.0.1"},
	}

	err = m.UpdateVirtualHost(deployment)
	if err != nil {
		t.Fatalf("UpdateVirtualHost failed: %v", err)
	}

	content, err = os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("failed to read updated config file: %v", err)
	}

	if !strings.Contains(string(content), "log_by_lua_block") {
		t.Error("Updated config should contain log_by_lua_block after enabling security")
	}
	if !strings.Contains(string(content), "deny 10.0.0.1;") {
		t.Error("Updated config should contain deny directive after enabling security")
	}

	// Disable security
	deployment.Metadata.Security.Enabled = false

	err = m.UpdateVirtualHost(deployment)
	if err != nil {
		t.Fatalf("UpdateVirtualHost (disable) failed: %v", err)
	}

	content, err = os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("failed to read re-updated config file: %v", err)
	}

	if strings.Contains(string(content), "log_by_lua_block") {
		t.Error("Config should NOT contain log_by_lua_block after disabling security")
	}
	if strings.Contains(string(content), "deny 10.0.0.1;") {
		t.Error("Config should NOT contain deny directive after disabling security")
	}
}

func TestSanitizeZoneName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"/api", "api"},
		{"/api/v1", "api_v1"},
		{"/api/v1/users", "api_v1_users"},
		{"/", "default"},
		{"", "default"},
		{"/api*", "api"},
		{"/api.json", "api_json"},
		{"/very/long/path/that/exceeds/twenty/characters", "very_long_path_that_"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := sanitizeZoneName(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeZoneName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestValidateSecurityHooks(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "nginx-validate-hooks-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	confDir := filepath.Join(tmpDir, "nginx", "conf.d")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		t.Fatalf("failed to create conf.d: %v", err)
	}

	cfg := &config.NginxConfig{}
	m := NewManager(cfg, tmpDir, "")

	t.Run("returns error when vhost does not exist", func(t *testing.T) {
		err := m.ValidateSecurityHooks("nonexistent", true)
		if err == nil {
			t.Error("expected error for nonexistent vhost")
		}
	})

	t.Run("validates hooks are present when expected", func(t *testing.T) {
		configWithHook := `server {
    listen 80;
    server_name test.example.com;

    location / {
        proxy_pass http://test:8080;
        log_by_lua_block {
            security.capture_event()
        }
    }
}`
		if err := os.WriteFile(filepath.Join(confDir, "with-hook.conf"), []byte(configWithHook), 0644); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}

		err := m.ValidateSecurityHooks("with-hook", true)
		if err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
	})

	t.Run("returns error when hooks missing but expected", func(t *testing.T) {
		configWithoutHook := `server {
    listen 80;
    server_name test.example.com;

    location / {
        proxy_pass http://test:8080;
    }
}`
		if err := os.WriteFile(filepath.Join(confDir, "without-hook.conf"), []byte(configWithoutHook), 0644); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}

		err := m.ValidateSecurityHooks("without-hook", true)
		if err == nil {
			t.Error("expected error when hooks are missing but expected")
		}
	})

	t.Run("validates hooks are absent when not expected", func(t *testing.T) {
		configWithoutHook := `server {
    listen 80;
    server_name test.example.com;

    location / {
        proxy_pass http://test:8080;
    }
}`
		if err := os.WriteFile(filepath.Join(confDir, "clean.conf"), []byte(configWithoutHook), 0644); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}

		err := m.ValidateSecurityHooks("clean", false)
		if err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
	})

	t.Run("returns error when hooks present but not expected", func(t *testing.T) {
		configWithHook := `server {
    listen 80;
    server_name test.example.com;

    location / {
        proxy_pass http://test:8080;
        log_by_lua_block {
            security.capture_event()
        }
    }
}`
		if err := os.WriteFile(filepath.Join(confDir, "unwanted-hook.conf"), []byte(configWithHook), 0644); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}

		err := m.ValidateSecurityHooks("unwanted-hook", false)
		if err == nil {
			t.Error("expected error when hooks present but not expected")
		}
	})
}

func TestGetSecurityHookStatus(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "nginx-hook-status-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	confDir := filepath.Join(tmpDir, "nginx", "conf.d")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		t.Fatalf("failed to create conf.d: %v", err)
	}

	cfg := &config.NginxConfig{}
	m := NewManager(cfg, tmpDir, "")

	t.Run("returns status for vhost with hooks", func(t *testing.T) {
		configWithHook := `server {
    listen 80;
    server_name test.example.com;

    location / {
        proxy_pass http://test:8080;
        log_by_lua_block {
            security.capture_event()
        }
    }

    location /api {
        proxy_pass http://test:8080;
        log_by_lua_block {
            security.capture_event()
        }
    }
}`
		if err := os.WriteFile(filepath.Join(confDir, "multi-hook.conf"), []byte(configWithHook), 0644); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}

		status, err := m.GetSecurityHookStatus("multi-hook")
		if err != nil {
			t.Fatalf("GetSecurityHookStatus failed: %v", err)
		}

		if !status.HasHooks {
			t.Error("expected HasHooks to be true")
		}
		if len(status.HookLocations) != 2 {
			t.Errorf("expected 2 hook locations, got %d", len(status.HookLocations))
		}
		if !status.ProperlyConfigured {
			t.Error("expected ProperlyConfigured to be true")
		}
	})

	t.Run("returns status for vhost without hooks", func(t *testing.T) {
		configWithoutHook := `server {
    listen 80;
    server_name test.example.com;

    location / {
        proxy_pass http://test:8080;
    }
}`
		if err := os.WriteFile(filepath.Join(confDir, "no-hook.conf"), []byte(configWithoutHook), 0644); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}

		status, err := m.GetSecurityHookStatus("no-hook")
		if err != nil {
			t.Fatalf("GetSecurityHookStatus failed: %v", err)
		}

		if status.HasHooks {
			t.Error("expected HasHooks to be false")
		}
		if len(status.HookLocations) != 0 {
			t.Errorf("expected 0 hook locations, got %d", len(status.HookLocations))
		}
		if status.ProperlyConfigured {
			t.Error("expected ProperlyConfigured to be false")
		}
	})
}

func TestUpdateDeploymentRateLimits(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "nginx-rate-limits-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	confDir := filepath.Join(tmpDir, "nginx", "conf.d")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		t.Fatalf("failed to create conf.d: %v", err)
	}

	cfg := &config.NginxConfig{}
	m := NewManager(cfg, tmpDir, "")

	// Add rate limits for first deployment
	rateLimits1 := []models.DeploymentRateLimit{
		{Path: "/api", Rate: 60, Burst: 10, Enabled: true},
		{Path: "/login", Rate: 10, Burst: 5, Enabled: true},
	}

	err = m.UpdateDeploymentRateLimits("app1", rateLimits1)
	if err != nil {
		t.Fatalf("UpdateDeploymentRateLimits failed: %v", err)
	}

	rateLimitsFile := filepath.Join(confDir, "rate_limits.conf")
	content, err := os.ReadFile(rateLimitsFile)
	if err != nil {
		t.Fatalf("failed to read rate_limits.conf: %v", err)
	}

	if !strings.Contains(string(content), "# Deployment: app1") {
		t.Error("rate_limits.conf should contain deployment header")
	}
	if !strings.Contains(string(content), "zone=app1_api") {
		t.Error("rate_limits.conf should contain app1_api zone")
	}
	if !strings.Contains(string(content), "zone=app1_login") {
		t.Error("rate_limits.conf should contain app1_login zone")
	}

	// Add rate limits for second deployment
	rateLimits2 := []models.DeploymentRateLimit{
		{Path: "/webhook", Rate: 100, Burst: 20, Enabled: true},
	}

	err = m.UpdateDeploymentRateLimits("app2", rateLimits2)
	if err != nil {
		t.Fatalf("UpdateDeploymentRateLimits for app2 failed: %v", err)
	}

	content, err = os.ReadFile(rateLimitsFile)
	if err != nil {
		t.Fatalf("failed to read rate_limits.conf: %v", err)
	}

	// Both deployments should be present
	if !strings.Contains(string(content), "# Deployment: app1") {
		t.Error("rate_limits.conf should still contain app1")
	}
	if !strings.Contains(string(content), "# Deployment: app2") {
		t.Error("rate_limits.conf should contain app2")
	}
	if !strings.Contains(string(content), "zone=app2_webhook") {
		t.Error("rate_limits.conf should contain app2_webhook zone")
	}

	// Remove rate limits for app1
	err = m.RemoveDeploymentRateLimits("app1")
	if err != nil {
		t.Fatalf("RemoveDeploymentRateLimits failed: %v", err)
	}

	content, err = os.ReadFile(rateLimitsFile)
	if err != nil {
		t.Fatalf("failed to read rate_limits.conf after removal: %v", err)
	}

	if strings.Contains(string(content), "# Deployment: app1") {
		t.Error("rate_limits.conf should NOT contain app1 after removal")
	}
	if !strings.Contains(string(content), "# Deployment: app2") {
		t.Error("rate_limits.conf should still contain app2")
	}
}

func TestWriteVirtualHost(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "nginx-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m := NewManager(&config.NginxConfig{
		ConfigPath: tmpDir,
	}, "/deployments", "")

	content := "# test nginx config\nserver { listen 80; }"
	if err := m.WriteVirtualHost("test-app", content); err != nil {
		t.Fatalf("WriteVirtualHost failed: %v", err)
	}

	configFile := filepath.Join(tmpDir, "test-app.conf")
	data, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	if string(data) != content {
		t.Errorf("expected content %q, got %q", content, string(data))
	}

	readContent, err := m.GetVirtualHost("test-app")
	if err != nil {
		t.Fatalf("GetVirtualHost failed: %v", err)
	}

	if readContent != content {
		t.Errorf("GetVirtualHost returned %q, expected %q", readContent, content)
	}
}

func TestIsNginxConfigValid(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "valid config no warnings",
			output: "nginx: the configuration file /etc/nginx/nginx.conf syntax is ok\nnginx: configuration file /etc/nginx/nginx.conf test is successful",
			want:   true,
		},
		{
			name:   "valid config with ssl_stapling warning",
			output: "2026/02/03 17:33:27 [warn] 2572#2572: \"ssl_stapling\" ignored\nnginx: the configuration file /etc/nginx/nginx.conf syntax is ok\nnginx: configuration file /etc/nginx/nginx.conf test is successful",
			want:   true,
		},
		{
			name:   "invalid config with emerg error",
			output: "nginx: [emerg] unknown directive \"invalid\" in /etc/nginx/conf.d/test.conf:1\nnginx: configuration file /etc/nginx/nginx.conf test failed",
			want:   false,
		},
		{
			name:   "invalid config with error",
			output: "nginx: [error] cannot load certificate\nnginx: configuration file /etc/nginx/nginx.conf test failed",
			want:   false,
		},
		{
			name:   "no success indicator",
			output: "[warn] some warning",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isNginxConfigValid(tt.output)
			if got != tt.want {
				t.Errorf("isNginxConfigValid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGroupDomainsByHost_DeduplicatesLocations(t *testing.T) {
	m := NewManager(&config.NginxConfig{}, "/deployments", "")

	domains := []models.DomainConfig{
		{ID: "1", Domain: "example.com", PathPrefix: "", ContainerPort: 80, Service: "web"},
		{ID: "2", Domain: "example.com", PathPrefix: "", ContainerPort: 8080, Service: "api"},
		{ID: "3", Domain: "example.com", PathPrefix: "/api", ContainerPort: 3000, Service: "backend"},
	}

	servers := m.groupDomainsByHost(domains, "test-app")

	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}

	if len(servers[0].Locations) != 2 {
		t.Errorf("expected 2 unique locations, got %d", len(servers[0].Locations))
	}

	pathCounts := make(map[string]int)
	for _, loc := range servers[0].Locations {
		pathCounts[loc.Path]++
	}

	if pathCounts["/"] != 1 {
		t.Errorf("expected exactly 1 location for '/', got %d", pathCounts["/"])
	}
}
