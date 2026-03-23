package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flatrun/agent/pkg/config"
)

func TestLoadInfraMetadata_ValidTemplate(t *testing.T) {
	meta, err := loadInfraMetadata("infra/nginx")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if meta.Name != "Nginx" {
		t.Errorf("Expected name 'Nginx', got '%s'", meta.Name)
	}
	if meta.Type != "infrastructure" {
		t.Errorf("Expected type 'infrastructure', got '%s'", meta.Type)
	}
}

func TestLoadInfraMetadata_NonExistent(t *testing.T) {
	_, err := loadInfraMetadata("infra/nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent template")
	}
}

func TestLoadInfraMetadata_NotInfra(t *testing.T) {
	meta, err := loadInfraMetadata("wordpress")
	if err != nil {
		t.Skipf("wordpress template not embedded: %v", err)
	}
	if meta.Type == "infrastructure" {
		t.Error("wordpress should not be an infrastructure template")
	}
}

func TestWriteNginxFiles(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		DeploymentsPath: tmpDir,
		Infrastructure: config.InfrastructureConfig{
			DefaultProxyNetwork: "proxy",
		},
	}

	deployDir := filepath.Join(tmpDir, "nginx")
	if err := os.MkdirAll(deployDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := writeNginxFiles(cfg, deployDir); err != nil {
		t.Fatalf("writeNginxFiles failed: %v", err)
	}

	expectedFiles := []string{
		"nginx.conf",
		"conf.d/rate_limits.conf",
		"html/index.html",
	}
	for _, f := range expectedFiles {
		path := filepath.Join(deployDir, f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("Expected file %s to exist", f)
		}
	}

	expectedDirs := []string{"conf.d", "certs", "html", "lua"}
	for _, d := range expectedDirs {
		path := filepath.Join(deployDir, d)
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			t.Errorf("Expected directory %s to exist", d)
		} else if !info.IsDir() {
			t.Errorf("Expected %s to be a directory", d)
		}
	}
}

func TestWriteNginxFiles_PreservesExistingIndex(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		DeploymentsPath: tmpDir,
		Infrastructure: config.InfrastructureConfig{
			DefaultProxyNetwork: "proxy",
		},
	}

	deployDir := filepath.Join(tmpDir, "nginx")
	htmlDir := filepath.Join(deployDir, "html")
	if err := os.MkdirAll(htmlDir, 0755); err != nil {
		t.Fatal(err)
	}

	customContent := []byte("<html>custom</html>")
	if err := os.WriteFile(filepath.Join(htmlDir, "index.html"), customContent, 0644); err != nil {
		t.Fatal(err)
	}

	if err := writeNginxFiles(cfg, deployDir); err != nil {
		t.Fatalf("writeNginxFiles failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(htmlDir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(customContent) {
		t.Error("writeNginxFiles should not overwrite existing index.html")
	}
}

func TestWriteNginxFiles_PreservesExistingRateLimits(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		DeploymentsPath: tmpDir,
		Infrastructure: config.InfrastructureConfig{
			DefaultProxyNetwork: "proxy",
		},
	}

	deployDir := filepath.Join(tmpDir, "nginx")
	confDir := filepath.Join(deployDir, "conf.d")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		t.Fatal(err)
	}

	customContent := []byte("limit_req_zone $binary_remote_addr zone=one:10m rate=1r/s;")
	if err := os.WriteFile(filepath.Join(confDir, "rate_limits.conf"), customContent, 0644); err != nil {
		t.Fatal(err)
	}

	if err := writeNginxFiles(cfg, deployDir); err != nil {
		t.Fatalf("writeNginxFiles failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(confDir, "rate_limits.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(customContent) {
		t.Error("writeNginxFiles should not overwrite existing rate_limits.conf")
	}
}

func TestDeployInfraService_InvalidTemplate(t *testing.T) {
	cfg := &config.Config{
		DeploymentsPath: t.TempDir(),
		Infrastructure: config.InfrastructureConfig{
			DefaultProxyNetwork: "proxy",
		},
	}

	err := deployInfraService(cfg, "nonexistent", "infra/nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent template")
	}
}
