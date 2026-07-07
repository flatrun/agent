package ssl

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
)

type ServiceExecutor interface {
	Execute(cfg *config.ServiceExecConfig, args []string) ([]byte, error)
}

type dockerServiceExecutor struct{}

func (e *dockerServiceExecutor) Execute(cfg *config.ServiceExecConfig, args []string) ([]byte, error) {
	return docker.ExecuteService(cfg, args)
}

type Manager struct {
	config           *config.CertbotConfig
	certsPath        string
	webRoot          string
	containerWebRoot string
	executor         ServiceExecutor
	mu               sync.RWMutex
}

func NewManager(cfg *config.CertbotConfig, deploymentsPath string, executor ServiceExecutor) *Manager {
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

	if executor == nil {
		executor = &dockerServiceExecutor{}
	}

	return &Manager{
		config:           cfg,
		certsPath:        certsPath,
		webRoot:          webRoot,
		containerWebRoot: containerWebRoot,
		executor:         executor,
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
		log.Printf("warning: skipping SSL for %s — certbot email not configured (set it in Settings)", domain)
		return &CertificateResult{
			Domain:  domain,
			Success: false,
			Message: "certbot email not configured — configure it in Settings to enable SSL",
		}, nil
	}

	if err := os.MkdirAll(m.webRoot, 0755); err != nil {
		return nil, fmt.Errorf("failed to create webroot: %w", err)
	}

	certbotArgs := []string{
		"certonly",
		"--non-interactive",
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
	return m.executor.Execute(cfg, args)
}

func (m *Manager) RenewCertificates() (*RenewalResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	output, err := m.executeCertbot([]string{"renew", "--non-interactive"})
	if err != nil {
		return nil, fmt.Errorf("renewal failed: %s - %w", string(output), err)
	}

	return &RenewalResult{
		Success: true,
		Message: string(output),
	}, nil
}

func (m *Manager) RenewCertificate(domain string, force bool) (*RenewalResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.certificateExistsLocked(domain) {
		return nil, fmt.Errorf("certificate for domain %q not found", domain)
	}

	args := []string{"renew", "--non-interactive", "--cert-name", domain}
	if force {
		args = append(args, "--force-renewal")
	}
	output, err := m.executeCertbot(args)
	if err != nil {
		return nil, fmt.Errorf("renewal failed for %s: %s - %w", domain, string(output), err)
	}

	// Without --force-renewal certbot skips a not-yet-due cert and still exits 0,
	// so distinguish an actual reissue from a no-op instead of always claiming success.
	renewed := force || certbotDidRenew(string(output))
	result := &RenewalResult{
		Success: true,
		Renewed: renewed,
		Message: string(output),
	}
	if renewed {
		result.RenewedDomains = []string{domain}
	}
	return result, nil
}

// certbotDidRenew reports whether a `certbot renew` run actually reissued a cert,
// by looking for the phrases certbot prints when it skips a not-yet-due lineage.
func certbotDidRenew(output string) bool {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "no renewals were attempted"),
		strings.Contains(lower, "not yet due for renewal"),
		strings.Contains(lower, "no certificates are due for renewal"):
		return false
	default:
		return true
	}
}

func (m *Manager) certificateExistsLocked(domain string) bool {
	certPath := filepath.Join(m.certsPath, domain, "cert.pem")
	_, err := os.Stat(certPath)
	return err == nil
}

// Per-certificate auto-renew markers. An explicit marker overrides the global
// default in either direction; a certificate with neither marker follows the
// global default. Only one marker is ever present at a time.
const (
	autoRenewEnabledMarker  = ".flatrun-auto-renew-enabled"
	autoRenewDisabledMarker = ".flatrun-auto-renew-disabled"
)

func (m *Manager) SetAutoRenew(domain string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.certificateExistsLocked(domain) {
		return fmt.Errorf("certificate for domain %q not found", domain)
	}

	dir := filepath.Join(m.certsPath, domain)
	write := filepath.Join(dir, autoRenewDisabledMarker)
	remove := filepath.Join(dir, autoRenewEnabledMarker)
	if enabled {
		write, remove = remove, write
	}

	if err := os.Remove(remove); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to set auto-renew: %w", err)
	}
	f, err := os.Create(write)
	if err != nil {
		return fmt.Errorf("failed to set auto-renew: %w", err)
	}
	return f.Close()
}

// isAutoRenewEnabled reports whether a certificate should auto-renew. An explicit
// per-certificate marker wins over the global default in both directions.
func (m *Manager) isAutoRenewEnabled(domain string) bool {
	dir := filepath.Join(m.certsPath, domain)
	if _, err := os.Stat(filepath.Join(dir, autoRenewDisabledMarker)); err == nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, autoRenewEnabledMarker)); err == nil {
		return true
	}
	return m.config.AutoRenewalEnabled == nil || *m.config.AutoRenewalEnabled
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

// HasOCSPResponder reports whether the domain's certificate advertises an OCSP
// responder URL. It returns false when the certificate is missing or cannot be
// parsed. Used to decide whether enabling ssl_stapling is worthwhile, since
// stapling on a cert with no responder only produces nginx warnings.
func (m *Manager) HasOCSPResponder(domain string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	certPath := filepath.Join(m.certsPath, domain, "cert.pem")
	data, err := os.ReadFile(certPath)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	return len(cert.OCSPServer) > 0
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
		AutoRenew: m.isAutoRenewEnabled(domain),
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
	Renewed        bool     `json:"renewed"`
	Message        string   `json:"message"`
	RenewedDomains []string `json:"renewed_domains,omitempty"`
}

type MultiCertificateResult struct {
	Results []*CertificateResult `json:"results"`
	Success bool                 `json:"success"`
}

func (m *Manager) RequestCertificatesForDomains(domains []models.DomainConfig) (*MultiCertificateResult, error) {
	// AutoCert is intentionally checked independently of SSL.Enabled:
	// the orchestrator temporarily disables Enabled before certs exist,
	// then re-enables it once obtained.
	domainSet := make(map[string]bool)
	for _, d := range domains {
		if d.SSL.AutoCert && d.Domain != "" {
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
		if d.SSL.AutoCert && d.Domain != "" {
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
