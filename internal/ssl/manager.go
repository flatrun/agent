package ssl

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
)

type Manager struct {
	config           *config.CertbotConfig
	certsPath        string
	webRoot          string
	containerWebRoot string
	mu               sync.RWMutex
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

	containerWebRoot := cfg.ContainerWebrootPath
	if containerWebRoot == "" {
		containerWebRoot = "/var/www/certbot"
	}

	return &Manager{
		config:           cfg,
		certsPath:        certsPath,
		webRoot:          webRoot,
		containerWebRoot: containerWebRoot,
	}
}

func (m *Manager) CertsPath() string {
	return m.certsPath
}

func (m *Manager) UpdateConfig(cfg *config.CertbotConfig, deploymentsPath string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.config = cfg

	certsPath := cfg.CertsPath
	if certsPath == "" {
		certsPath = filepath.Join(deploymentsPath, "nginx", "certs", "live")
	}
	m.certsPath = certsPath

	webRoot := cfg.WebrootPath
	if webRoot == "" {
		webRoot = filepath.Join(deploymentsPath, "nginx", "html")
	}
	m.webRoot = webRoot

	containerWebRoot := cfg.ContainerWebrootPath
	if containerWebRoot == "" {
		containerWebRoot = "/var/www/certbot"
	}
	m.containerWebRoot = containerWebRoot
}

func (m *Manager) RequestCertificate(domain string) (*CertificateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.config.Email == "" {
		return nil, fmt.Errorf("certbot email not configured")
	}

	if err := os.MkdirAll(m.webRoot, 0755); err != nil {
		return nil, fmt.Errorf("failed to create webroot: %w", err)
	}

	certbotArgs := []string{
		"certonly",
		"--webroot",
		"--webroot-path", m.containerWebRoot,
		"--email", m.config.Email,
		"--agree-tos",
		"--no-eff-email",
		"-d", domain,
	}

	if m.config.Staging {
		certbotArgs = append(certbotArgs, "--staging")
	}

	output, err := m.executeCertbot(certbotArgs)
	if err != nil {
		return nil, fmt.Errorf("certbot failed: %s - %w", string(output), err)
	}

	return &CertificateResult{
		Domain:  domain,
		Success: true,
		Message: string(output),
	}, nil
}

func (m *Manager) getServiceExecConfig() *config.ServiceExecConfig {
	image := m.config.Image
	if image == "" {
		image = "certbot/certbot"
	}

	certsDir := filepath.Dir(m.certsPath)

	return &config.ServiceExecConfig{
		Image:        image,
		KeepAlive:    false,
		RunOnRequest: true,
		Volumes: []string{
			certsDir + ":/etc/letsencrypt",
			m.webRoot + ":" + m.containerWebRoot,
		},
	}
}

func (m *Manager) executeCertbot(args []string) ([]byte, error) {
	cfg := m.getServiceExecConfig()
	return docker.ExecuteService(cfg, args)
}

func (m *Manager) RenewCertificates() (*RenewalResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	output, err := m.executeCertbot([]string{"renew"})
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

	certPath := fmt.Sprintf("/etc/letsencrypt/live/%s/cert.pem", domain)
	output, err := m.executeCertbot([]string{"revoke", "--cert-path", certPath, "--non-interactive"})
	if err != nil {
		return fmt.Errorf("revocation failed: %s - %w", string(output), err)
	}

	return nil
}

func (m *Manager) DeleteCertificate(domain string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	output, err := m.executeCertbot([]string{"delete", "--cert-name", domain, "--non-interactive"})
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
	Success        bool     `json:"success"`
	Message        string   `json:"message"`
	RenewedDomains []string `json:"renewed_domains,omitempty"`
}

type MultiCertificateResult struct {
	Results []*CertificateResult `json:"results"`
	Success bool                 `json:"success"`
}

func (m *Manager) RequestCertificatesForDomains(domains []models.DomainConfig) (*MultiCertificateResult, error) {
	domainSet := make(map[string]bool)
	for _, d := range domains {
		if d.SSL.Enabled && d.SSL.AutoCert && d.Domain != "" {
			domainSet[d.Domain] = true
			for _, alias := range d.Aliases {
				domainSet[alias] = true
			}
		}
	}

	result := &MultiCertificateResult{
		Results: make([]*CertificateResult, 0),
		Success: true,
	}

	for domain := range domainSet {
		if m.CertificateExists(domain) {
			result.Results = append(result.Results, &CertificateResult{
				Domain:  domain,
				Success: true,
				Message: "Certificate already exists",
			})
			continue
		}

		certResult, err := m.RequestCertificate(domain)
		if err != nil {
			result.Results = append(result.Results, &CertificateResult{
				Domain:  domain,
				Success: false,
				Message: err.Error(),
			})
			result.Success = false
		} else {
			result.Results = append(result.Results, certResult)
		}
	}

	return result, nil
}

func (m *Manager) GetDomainsNeedingCertificates(domains []models.DomainConfig) []string {
	var result []string
	for _, d := range domains {
		if d.SSL.Enabled && d.SSL.AutoCert && d.Domain != "" {
			if !m.CertificateExists(d.Domain) {
				result = append(result, d.Domain)
			}
			for _, alias := range d.Aliases {
				if !m.CertificateExists(alias) {
					result = append(result, alias)
				}
			}
		}
	}
	return result
}
