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
