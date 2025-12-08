package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/flatrun/agent/internal/nginx"
	"github.com/flatrun/agent/internal/proxy"
	"github.com/flatrun/agent/internal/ssl"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
)

func TestDeleteCertificate_BlockedWhenInUse(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ssl-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	confDir := filepath.Join(tmpDir, "nginx", "conf.d")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		t.Fatalf("failed to create conf.d: %v", err)
	}

	certsDir := filepath.Join(tmpDir, "certs", "live", "example.com")
	if err := os.MkdirAll(certsDir, 0755); err != nil {
		t.Fatalf("failed to create certs dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(certsDir, "cert.pem"), []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create cert file: %v", err)
	}

	sslConfig := `server {
    listen 443 ssl;
    server_name example.com;
    ssl_certificate /etc/letsencrypt/live/example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/example.com/privkey.pem;
}`
	if err := os.WriteFile(filepath.Join(confDir, "my-app.conf"), []byte(sslConfig), 0644); err != nil {
		t.Fatalf("failed to create ssl config: %v", err)
	}

	cfg := &config.Config{
		DeploymentsPath: tmpDir,
		Nginx:           config.NginxConfig{},
		Certbot: config.CertbotConfig{
			CertsPath: filepath.Join(tmpDir, "certs", "live"),
		},
	}

	nginxMgr := nginx.NewManager(&cfg.Nginx, tmpDir, "")
	sslMgr := ssl.NewManager(&cfg.Certbot, tmpDir)

	orchestrator := proxy.NewOrchestratorWithManagers(nginxMgr, sslMgr)

	gin.SetMode(gin.TestMode)
	router := gin.New()

	server := &Server{
		config:            cfg,
		proxyOrchestrator: orchestrator,
	}

	router.DELETE("/certificates/:domain", server.deleteCertificate)

	req := httptest.NewRequest(http.MethodDelete, "/certificates/example.com", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected status 409, got %d", w.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if response["error"] != "Certificate is in use by virtual hosts" {
		t.Errorf("unexpected error message: %v", response["error"])
	}

	vhosts, ok := response["vhosts"].([]interface{})
	if !ok {
		t.Fatalf("expected vhosts array in response")
	}
	if len(vhosts) != 1 {
		t.Errorf("expected 1 vhost, got %d", len(vhosts))
	}
	if vhosts[0] != "my-app" {
		t.Errorf("expected vhost 'my-app', got %v", vhosts[0])
	}
}

func TestDeleteCertificate_NotBlockedWhenNotInUse(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ssl-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	confDir := filepath.Join(tmpDir, "nginx", "conf.d")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		t.Fatalf("failed to create conf.d: %v", err)
	}

	certsDir := filepath.Join(tmpDir, "certs", "live", "unused.com")
	if err := os.MkdirAll(certsDir, 0755); err != nil {
		t.Fatalf("failed to create certs dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(certsDir, "cert.pem"), []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create cert file: %v", err)
	}

	httpConfig := `server {
    listen 80;
    server_name other.com;
}`
	if err := os.WriteFile(filepath.Join(confDir, "http-app.conf"), []byte(httpConfig), 0644); err != nil {
		t.Fatalf("failed to create http config: %v", err)
	}

	cfg := &config.Config{
		DeploymentsPath: tmpDir,
		Nginx:           config.NginxConfig{},
		Certbot: config.CertbotConfig{
			CertsPath: filepath.Join(tmpDir, "certs", "live"),
		},
	}

	nginxMgr := nginx.NewManager(&cfg.Nginx, tmpDir, "")

	vhosts := nginxMgr.GetVhostsUsingSSLDomain("unused.com")
	if len(vhosts) != 0 {
		t.Errorf("expected no vhosts using unused.com, got %v", vhosts)
	}
}

func TestDeleteCertificate_DetectsUsage(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ssl-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	confDir := filepath.Join(tmpDir, "nginx", "conf.d")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		t.Fatalf("failed to create conf.d: %v", err)
	}

	certsDir := filepath.Join(tmpDir, "certs", "live", "used.com")
	if err := os.MkdirAll(certsDir, 0755); err != nil {
		t.Fatalf("failed to create certs dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(certsDir, "cert.pem"), []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create cert file: %v", err)
	}

	sslConfig := `server {
    listen 443 ssl;
    server_name used.com;
    ssl_certificate /etc/letsencrypt/live/used.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/used.com/privkey.pem;
}`
	if err := os.WriteFile(filepath.Join(confDir, "ssl-app.conf"), []byte(sslConfig), 0644); err != nil {
		t.Fatalf("failed to create ssl config: %v", err)
	}

	cfg := &config.Config{
		DeploymentsPath: tmpDir,
		Nginx:           config.NginxConfig{},
		Certbot: config.CertbotConfig{
			CertsPath: filepath.Join(tmpDir, "certs", "live"),
		},
	}

	nginxMgr := nginx.NewManager(&cfg.Nginx, tmpDir, "")

	vhosts := nginxMgr.GetVhostsUsingSSLDomain("used.com")
	if len(vhosts) != 1 {
		t.Errorf("expected 1 vhost using used.com, got %d", len(vhosts))
	}
	if len(vhosts) > 0 && vhosts[0] != "ssl-app" {
		t.Errorf("expected vhost 'ssl-app', got %v", vhosts[0])
	}
}
