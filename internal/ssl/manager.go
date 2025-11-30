package ssl

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
)

type Manager struct {
	config    *config.CertbotConfig
	certsPath string
	webRoot   string
	mu        sync.RWMutex
}

func NewManager(cfg *config.CertbotConfig, deploymentsPath string) *Manager {
	certsPath := cfg.CertsPath
	if certsPath == "" {
		certsPath = filepath.Join(deploymentsPath, "nginx", "certs", "live")
	}

	webRoot := cfg.WebrootPath
	if webRoot == "" {
		webRoot = filepath.Join(deploymentsPath, "nginx", "html")
	}

	return &Manager{
		config:    cfg,
		certsPath: certsPath,
		webRoot:   webRoot,
	}
}

func (m *Manager) CertsPath() string {
	return m.certsPath
}

func (m *Manager) RequestCertificate(domain string) (*CertificateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.config.ContainerName == "" {
		return nil, fmt.Errorf("certbot container name not configured")
	}

	if m.config.Email == "" {
		return nil, fmt.Errorf("certbot email not configured")
	}

	if err := os.MkdirAll(m.webRoot, 0755); err != nil {
		return nil, fmt.Errorf("failed to create webroot: %w", err)
	}

	args := []string{
		"exec", m.config.ContainerName,
		"certbot", "certonly",
		"--webroot",
		"--webroot-path=/var/www/certbot",
		"--email", m.config.Email,
		"--agree-tos",
		"--no-eff-email",
		"-d", domain,
	}

	if m.config.Staging {
		args = append(args, "--staging")
	}

	cmd := exec.Command("docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("certbot failed: %s - %w", string(output), err)
	}

	return &CertificateResult{
		Domain:  domain,
		Success: true,
		Message: string(output),
	}, nil
}

func (m *Manager) RenewCertificates() (*RenewalResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.config.ContainerName == "" {
		return nil, fmt.Errorf("certbot container name not configured")
	}

	cmd := exec.Command("docker", "exec", m.config.ContainerName, "certbot", "renew")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("renewal failed: %s - %w", string(output), err)
	}

	return &RenewalResult{
		Success: true,
		Message: string(output),
	}, nil
}

func (m *Manager) RevokeCertificate(domain string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.config.ContainerName == "" {
		return fmt.Errorf("certbot container name not configured")
	}

	certPath := fmt.Sprintf("/etc/letsencrypt/live/%s/cert.pem", domain)

	cmd := exec.Command("docker", "exec", m.config.ContainerName,
		"certbot", "revoke", "--cert-path", certPath, "--non-interactive")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("revocation failed: %s - %w", string(output), err)
	}

	return nil
}

func (m *Manager) DeleteCertificate(domain string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.config.ContainerName == "" {
		return fmt.Errorf("certbot container name not configured")
	}

	cmd := exec.Command("docker", "exec", m.config.ContainerName,
		"certbot", "delete", "--cert-name", domain, "--non-interactive")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("deletion failed: %s - %w", string(output), err)
	}

	return nil
}

func (m *Manager) GetCertificate(domain string) (*models.Certificate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	certPath := filepath.Join(m.certsPath, domain, "cert.pem")
	return m.parseCertificate(certPath, domain)
}

func (m *Manager) ListCertificates() ([]models.Certificate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var certificates []models.Certificate

	if _, err := os.Stat(m.certsPath); os.IsNotExist(err) {
		return certificates, nil
	}

	entries, err := os.ReadDir(m.certsPath)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		certPath := filepath.Join(m.certsPath, entry.Name(), "cert.pem")
		cert, err := m.parseCertificate(certPath, entry.Name())
		if err != nil {
			continue
		}

		certificates = append(certificates, *cert)
	}

	return certificates, nil
}

func (m *Manager) CertificateExists(domain string) bool {
	certPath := filepath.Join(m.certsPath, domain, "cert.pem")
	_, err := os.Stat(certPath)
	return err == nil
}

func (m *Manager) GetExpiringCertificates(daysThreshold int) ([]models.Certificate, error) {
	certs, err := m.ListCertificates()
	if err != nil {
		return nil, err
	}

	var expiring []models.Certificate
	for _, cert := range certs {
		if cert.DaysLeft <= daysThreshold {
			expiring = append(expiring, cert)
		}
	}

	return expiring, nil
}

func (m *Manager) parseCertificate(certPath, domain string) (*models.Certificate, error) {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	daysLeft := int(cert.NotAfter.Sub(now).Hours() / 24)

	status := "valid"
	if now.After(cert.NotAfter) {
		status = "expired"
	} else if daysLeft <= 30 {
		status = "expiring"
	}

	issuer := cert.Issuer.CommonName
	if issuer == "" && len(cert.Issuer.Organization) > 0 {
		issuer = cert.Issuer.Organization[0]
	}

	return &models.Certificate{
		Domain:    domain,
		Issuer:    issuer,
		NotBefore: cert.NotBefore,
		NotAfter:  cert.NotAfter,
		DaysLeft:  daysLeft,
		Status:    status,
		Path:      certPath,
		AutoRenew: true,
	}, nil
}

func (m *Manager) SetupAutoCertificate(domain string) error {
	if m.CertificateExists(domain) {
		return nil
	}

	_, err := m.RequestCertificate(domain)
	return err
}

func (m *Manager) ValidateDomain(domain string) error {
	if domain == "" {
		return fmt.Errorf("domain cannot be empty")
	}

	if strings.Contains(domain, " ") {
		return fmt.Errorf("domain cannot contain spaces")
	}

	parts := strings.Split(domain, ".")
	if len(parts) < 2 {
		return fmt.Errorf("invalid domain format")
	}

	return nil
}

type CertificateResult struct {
	Domain  string `json:"domain"`
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type RenewalResult struct {
	Success       bool     `json:"success"`
	Message       string   `json:"message"`
	RenewedDomains []string `json:"renewed_domains,omitempty"`
}
