package ssl

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/config"
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

func TestGetServiceExecConfig_WebrootVolume(t *testing.T) {
	tests := []struct {
		name            string
		webrootPath     string
		deploymentsPath string
		expectedVolume  string
	}{
		{
			name:            "configured webroot in volume mount",
			webrootPath:     "/custom/webroot",
			deploymentsPath: "/deployments",
			expectedVolume:  "/custom/webroot:/var/www/certbot",
		},
		{
			name:            "default webroot in volume mount",
			webrootPath:     "",
			deploymentsPath: "/deployments",
			expectedVolume:  "/deployments/nginx/html:/var/www/certbot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.CertbotConfig{
				WebrootPath: tt.webrootPath,
			}

			m := NewManager(cfg, tt.deploymentsPath)
			execCfg := m.getServiceExecConfig()

			found := false
			for _, vol := range execCfg.Volumes {
				if strings.Contains(vol, ":/var/www/certbot") {
					if vol != tt.expectedVolume {
						t.Errorf("volume = %q, want %q", vol, tt.expectedVolume)
					}
					found = true
					break
				}
			}

			if !found {
				t.Error("webroot volume mount not found in exec config")
			}
		})
	}
}
