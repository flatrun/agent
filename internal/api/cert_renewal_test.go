package api

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/nginx"
	"github.com/flatrun/agent/internal/proxy"
	"github.com/flatrun/agent/internal/ssl"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

type fakeCertbotExecutor struct{}

func (fakeCertbotExecutor) Execute(_ *config.ServiceExecConfig, _ []string) ([]byte, error) {
	return []byte("ok"), nil
}

func writeSelfSignedCert(t *testing.T, certsDir, domain string) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: domain},
		Issuer:       pkix.Name{CommonName: "flatrun-test-ca"},
		NotBefore:    time.Now().Add(-24 * time.Hour),
		NotAfter:     time.Now().Add(60 * 24 * time.Hour),
		DNSNames:     []string{domain},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	dir := filepath.Join(certsDir, domain)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(filepath.Join(dir, "cert.pem"))
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("encode: %v", err)
	}
}

func writeDeploymentWithDomains(t *testing.T, deploymentsPath, name string, domains []models.DomainConfig) {
	t.Helper()

	dir := filepath.Join(deploymentsPath, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir deployment: %v", err)
	}
	compose := "name: " + name + "\nservices:\n  web:\n    image: nginx:latest\n"
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatalf("write compose: %v", err)
	}

	meta := &models.ServiceMetadata{
		Name:    name,
		Type:    "web",
		Domains: domains,
	}
	data, err := yaml.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "service.yml"), data, 0644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
}

func setupRenewalTestServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	deploymentsPath := filepath.Join(tmpDir, "deployments")
	certsPath := filepath.Join(tmpDir, "certs", "live")
	if err := os.MkdirAll(deploymentsPath, 0755); err != nil {
		t.Fatalf("mkdir deployments: %v", err)
	}
	if err := os.MkdirAll(certsPath, 0755); err != nil {
		t.Fatalf("mkdir certs: %v", err)
	}

	cfg := &config.Config{
		DeploymentsPath: deploymentsPath,
		Certbot:         config.CertbotConfig{CertsPath: certsPath},
	}

	nginxMgr := nginx.NewManager(&cfg.Nginx, deploymentsPath, "")
	sslMgr := ssl.NewManager(&cfg.Certbot, deploymentsPath, fakeCertbotExecutor{})
	orch := proxy.NewOrchestratorWithManagers(nginxMgr, sslMgr)

	server := &Server{
		config:            cfg,
		manager:           docker.NewManager(deploymentsPath),
		proxyOrchestrator: orch,
	}

	return server, deploymentsPath, certsPath
}

func TestListCertificates_AnnotatesDeploymentID(t *testing.T) {
	server, deploymentsPath, certsPath := setupRenewalTestServer(t)

	writeSelfSignedCert(t, certsPath, "app.example.com")
	writeSelfSignedCert(t, certsPath, "alias.example.com")
	writeSelfSignedCert(t, certsPath, "orphan.example.com")

	writeDeploymentWithDomains(t, deploymentsPath, "my-app", []models.DomainConfig{
		{
			Domain:  "app.example.com",
			Aliases: []string{"alias.example.com"},
		},
	})

	router := gin.New()
	router.GET("/certificates", server.listCertificates)

	req := httptest.NewRequest(http.MethodGet, "/certificates", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}

	var resp struct {
		Certificates []models.Certificate `json:"certificates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	byDomain := make(map[string]string)
	for _, c := range resp.Certificates {
		byDomain[c.Domain] = c.DeploymentID
	}

	if byDomain["app.example.com"] != "my-app" {
		t.Errorf("app.example.com DeploymentID = %q, want my-app", byDomain["app.example.com"])
	}
	if byDomain["alias.example.com"] != "my-app" {
		t.Errorf("alias.example.com DeploymentID = %q, want my-app (via alias)", byDomain["alias.example.com"])
	}
	if byDomain["orphan.example.com"] != "" {
		t.Errorf("orphan cert should have empty DeploymentID, got %q", byDomain["orphan.example.com"])
	}
}

func TestRenewDeploymentCertificates_CollectsAllDomainsAndAliases(t *testing.T) {
	server, deploymentsPath, certsPath := setupRenewalTestServer(t)

	writeSelfSignedCert(t, certsPath, "primary.example.com")
	writeSelfSignedCert(t, certsPath, "alt.example.com")
	writeSelfSignedCert(t, certsPath, "other.example.com")

	writeDeploymentWithDomains(t, deploymentsPath, "multi-app", []models.DomainConfig{
		{
			Domain:  "primary.example.com",
			Aliases: []string{"alt.example.com"},
		},
		{Domain: "other.example.com"},
	})

	router := gin.New()
	router.POST("/deployments/:name/certificates/renew", server.renewDeploymentCertificates)

	req := httptest.NewRequest(
		http.MethodPost,
		"/deployments/multi-app/certificates/renew",
		bytes.NewReader(nil),
	)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}

	var resp struct {
		Deployment string `json:"deployment"`
		Result     struct {
			Success bool `json:"success"`
			Results []struct {
				Domain  string `json:"domain"`
				Success bool   `json:"success"`
			} `json:"results"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if resp.Deployment != "multi-app" {
		t.Errorf("deployment = %q, want multi-app", resp.Deployment)
	}
	if len(resp.Result.Results) != 3 {
		t.Errorf("expected 3 domains renewed, got %d: %+v", len(resp.Result.Results), resp.Result.Results)
	}
	seen := make(map[string]bool)
	for _, r := range resp.Result.Results {
		seen[r.Domain] = true
	}
	for _, want := range []string{"primary.example.com", "alt.example.com", "other.example.com"} {
		if !seen[want] {
			t.Errorf("expected result for %s, got %+v", want, resp.Result.Results)
		}
	}
}

func TestRenewDeploymentCertificates_NoDomains(t *testing.T) {
	server, deploymentsPath, _ := setupRenewalTestServer(t)

	writeDeploymentWithDomains(t, deploymentsPath, "empty-app", nil)

	router := gin.New()
	router.POST("/deployments/:name/certificates/renew", server.renewDeploymentCertificates)

	req := httptest.NewRequest(
		http.MethodPost,
		"/deployments/empty-app/certificates/renew",
		bytes.NewReader(nil),
	)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for deployment with no domains, got %d (%s)", w.Code, w.Body.String())
	}
}
