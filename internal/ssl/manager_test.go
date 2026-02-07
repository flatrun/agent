package ssl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
)

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
		{
			name:            "uses default with different deployments path",
			webrootPath:     "",
			deploymentsPath: "/root/deployments",
			expectedWebroot: "/root/deployments/nginx/html",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.CertbotConfig{
				WebrootPath: tt.webrootPath,
			}

			m := NewManager(cfg, tt.deploymentsPath)

			if m.webRoot != tt.expectedWebroot {
				t.Errorf("webRoot = %q, want %q", m.webRoot, tt.expectedWebroot)
			}
		})
	}
}

func TestUpdateConfig_WebrootPath(t *testing.T) {
	cfg := &config.CertbotConfig{
		WebrootPath: "/initial/webroot",
	}
	m := NewManager(cfg, "/deployments")

	if m.webRoot != "/initial/webroot" {
		t.Errorf("initial webRoot = %q, want %q", m.webRoot, "/initial/webroot")
	}

	newCfg := &config.CertbotConfig{
		WebrootPath: "/updated/webroot",
	}
	m.UpdateConfig(newCfg, "/deployments")

	if m.webRoot != "/updated/webroot" {
		t.Errorf("updated webRoot = %q, want %q", m.webRoot, "/updated/webroot")
	}

	emptyCfg := &config.CertbotConfig{
		WebrootPath: "",
	}
	m.UpdateConfig(emptyCfg, "/new/deployments")

	expected := filepath.Join("/new/deployments", "nginx", "html")
	if m.webRoot != expected {
		t.Errorf("default webRoot = %q, want %q", m.webRoot, expected)
	}
}

func TestContainerWebrootPath(t *testing.T) {
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
			expectedContainerWebroot: "/var/www/certbot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.CertbotConfig{
				ContainerWebrootPath: tt.containerWebrootPath,
			}

			m := NewManager(cfg, "/deployments")

			if m.containerWebRoot != tt.expectedContainerWebroot {
				t.Errorf("containerWebRoot = %q, want %q", m.containerWebRoot, tt.expectedContainerWebroot)
			}
		})
	}
}

func TestGetDomainsNeedingCertificates_AutoCertWithDisabledSSL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ssl-cert-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.CertbotConfig{
		CertsPath: tmpDir,
	}
	m := NewManager(cfg, tmpDir)

	domains := []models.DomainConfig{
		{
			Domain: "new.example.com",
			SSL: models.SSLConfig{
				Enabled:  false,
				AutoCert: true,
			},
		},
	}

	result := m.GetDomainsNeedingCertificates(domains)

	if len(result) != 1 {
		t.Fatalf("expected 1 domain needing certificate, got %d", len(result))
	}
	if result[0] != "new.example.com" {
		t.Errorf("expected 'new.example.com', got '%s'", result[0])
	}
}

func TestGetDomainsNeedingCertificates_SkipsWithoutAutoCert(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ssl-cert-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.CertbotConfig{
		CertsPath: tmpDir,
	}
	m := NewManager(cfg, tmpDir)

	domains := []models.DomainConfig{
		{
			Domain: "manual.example.com",
			SSL: models.SSLConfig{
				Enabled:  true,
				AutoCert: false,
			},
		},
	}

	result := m.GetDomainsNeedingCertificates(domains)

	if len(result) != 0 {
		t.Errorf("expected no domains needing certificates for AutoCert=false, got %d", len(result))
	}
}

func TestGetServiceExecConfig_WebrootVolume(t *testing.T) {
	tests := []struct {
		name                 string
		webrootPath          string
		containerWebrootPath string
		deploymentsPath      string
		expectedVolume       string
	}{
		{
			name:                 "configured paths",
			webrootPath:          "/custom/webroot",
			containerWebrootPath: "/custom/container",
			deploymentsPath:      "/deployments",
			expectedVolume:       "/custom/webroot:/custom/container",
		},
		{
			name:                 "default paths",
			webrootPath:          "",
			containerWebrootPath: "",
			deploymentsPath:      "/deployments",
			expectedVolume:       "/deployments/nginx/html:/var/www/certbot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.CertbotConfig{
				WebrootPath:          tt.webrootPath,
				ContainerWebrootPath: tt.containerWebrootPath,
			}

			m := NewManager(cfg, tt.deploymentsPath)
			execCfg := m.getServiceExecConfig()

			found := false
			for _, vol := range execCfg.Volumes {
				if strings.Contains(vol, m.containerWebRoot) {
					if vol != tt.expectedVolume {
						t.Errorf("volume = %q, want %q", vol, tt.expectedVolume)
					}
					found = true
					break
				}
			}

			if !found {
				t.Errorf("webroot volume mount not found, expected %q", tt.expectedVolume)
			}
		})
	}
}
