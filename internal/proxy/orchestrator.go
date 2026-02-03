package proxy

import (
	"fmt"
	"log"

	"github.com/flatrun/agent/internal/nginx"
	"github.com/flatrun/agent/internal/ssl"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
)

type Orchestrator struct {
	nginx *nginx.Manager
	ssl   *ssl.Manager
}

func NewOrchestrator(cfg *config.Config) *Orchestrator {
	return &Orchestrator{
		nginx: nginx.NewManager(&cfg.Nginx, cfg.DeploymentsPath, cfg.Certbot.WebrootPath),
		ssl:   ssl.NewManager(&cfg.Certbot, cfg.DeploymentsPath),
	}
}

func NewOrchestratorWithManagers(nginxMgr *nginx.Manager, sslMgr *ssl.Manager) *Orchestrator {
	return &Orchestrator{
		nginx: nginxMgr,
		ssl:   sslMgr,
	}
}

func (o *Orchestrator) NginxManager() *nginx.Manager {
	return o.nginx
}

func (o *Orchestrator) SSLManager() *ssl.Manager {
	return o.ssl
}

func (o *Orchestrator) UpdateConfig(cfg *config.Config) {
	o.nginx.UpdateConfig(&cfg.Nginx, cfg.DeploymentsPath, cfg.Certbot.WebrootPath)
	o.ssl.UpdateConfig(&cfg.Certbot, cfg.DeploymentsPath)
}

func (o *Orchestrator) SetupDeployment(deployment *models.Deployment) (*SetupResult, error) {
	result := &SetupResult{
		DeploymentName: deployment.Name,
	}

	if deployment.Metadata == nil {
		result.Skipped = true
		result.Message = "deployment has no metadata"
		return result, nil
	}

	domains := deployment.Metadata.GetDomains()
	if len(domains) == 0 {
		result.Skipped = true
		result.Message = "deployment not configured for exposure"
		return result, nil
	}

	return o.setupMultiDomainDeployment(deployment, domains)
}

func (o *Orchestrator) setupMultiDomainDeployment(deployment *models.Deployment, domains []models.DomainConfig) (*SetupResult, error) {
	result := &SetupResult{
		DeploymentName: deployment.Name,
		Domains:        deployment.Metadata.GetUniqueDomainNames(),
	}

	for _, domainName := range result.Domains {
		if err := o.ssl.ValidateDomain(domainName); err != nil {
			return nil, fmt.Errorf("invalid domain %s: %w", domainName, err)
		}
	}

	if len(result.Domains) > 0 {
		result.Domain = result.Domains[0]
	}

	for i := range domains {
		if domains[i].SSL.Enabled && domains[i].SSL.AutoCert {
			if !o.ssl.CertificateExists(domains[i].Domain) {
				domains[i].SSL.Enabled = false
			}
		}
	}
	deployment.Metadata.Domains = domains

	var previousConfig string
	hadPreviousConfig := o.nginx.VirtualHostExists(deployment.Name)
	if hadPreviousConfig {
		var err error
		previousConfig, err = o.nginx.GetVirtualHost(deployment.Name)
		if err != nil {
			log.Printf("warning: failed to backup previous vhost config: %v", err)
			hadPreviousConfig = false
		}
	}

	if err := o.nginx.CreateVirtualHost(deployment); err != nil {
		return nil, fmt.Errorf("failed to create virtual host: %w", err)
	}
	result.VirtualHostCreated = true

	if err := o.nginx.TestConfig(); err != nil {
		if hadPreviousConfig {
			if restoreErr := o.nginx.WriteVirtualHost(deployment.Name, previousConfig); restoreErr != nil {
				log.Printf("warning: failed to restore previous vhost config: %v", restoreErr)
			}
		} else {
			_ = o.nginx.DeleteVirtualHost(deployment.Name)
		}
		return nil, fmt.Errorf("nginx config validation failed: %w", err)
	}

	if err := o.nginx.Reload(); err != nil {
		log.Printf("warning: failed to reload nginx: %v", err)
	} else {
		result.NginxReloaded = true
	}

	certResults, err := o.ssl.RequestCertificatesForDomains(domains)
	if err != nil {
		log.Printf("warning: failed to request certificates: %v", err)
		result.SSLError = err.Error()
	} else {
		result.CertificateResults = certResults.Results
		result.CertificateRequested = len(certResults.Results) > 0
		if certResults.Success {
			result.CertificateExists = true
		}

		needsUpdate := false
		for i := range domains {
			if domains[i].SSL.AutoCert && o.ssl.CertificateExists(domains[i].Domain) {
				domains[i].SSL.Enabled = true
				needsUpdate = true
			}
		}

		if needsUpdate {
			deployment.Metadata.Domains = domains
			if err := o.nginx.UpdateVirtualHost(deployment); err != nil {
				log.Printf("warning: failed to update virtual host with SSL: %v", err)
			}
			if err := o.nginx.TestConfig(); err != nil {
				log.Printf("warning: SSL config test failed: %v", err)
			}
			if err := o.nginx.Reload(); err != nil {
				log.Printf("warning: failed to reload nginx after SSL: %v", err)
			}
		}
	}

	result.Success = true
	return result, nil
}

func (o *Orchestrator) TeardownDeployment(deploymentName string) error {
	if err := o.nginx.DeleteVirtualHost(deploymentName); err != nil {
		return fmt.Errorf("failed to delete virtual host: %w", err)
	}

	if err := o.nginx.Reload(); err != nil {
		log.Printf("warning: failed to reload nginx after teardown: %v", err)
	}

	return nil
}

func (o *Orchestrator) UpdateDeployment(deployment *models.Deployment) (*SetupResult, error) {
	hasDomains := deployment.Metadata != nil && len(deployment.Metadata.GetDomains()) > 0

	if !hasDomains {
		if o.nginx.VirtualHostExists(deployment.Name) {
			if err := o.TeardownDeployment(deployment.Name); err != nil {
				return nil, err
			}
		}
		return &SetupResult{
			DeploymentName: deployment.Name,
			Skipped:        true,
			Message:        "deployment no longer exposed",
		}, nil
	}

	return o.SetupDeployment(deployment)
}

func (o *Orchestrator) RequestCertificate(domain string) (*ssl.CertificateResult, error) {
	if err := o.ssl.ValidateDomain(domain); err != nil {
		return nil, err
	}

	result, err := o.ssl.RequestCertificate(domain)
	if err != nil {
		return nil, err
	}

	if err := o.nginx.Reload(); err != nil {
		log.Printf("warning: failed to reload nginx after certificate request: %v", err)
	}

	return result, nil
}

func (o *Orchestrator) RenewCertificates() (*ssl.RenewalResult, error) {
	result, err := o.ssl.RenewCertificates()
	if err != nil {
		return nil, err
	}

	if err := o.nginx.Reload(); err != nil {
		log.Printf("warning: failed to reload nginx after renewal: %v", err)
	}

	return result, nil
}

func (o *Orchestrator) GetDeploymentProxyStatus(deployment *models.Deployment) *ProxyStatus {
	status := &ProxyStatus{
		DeploymentName: deployment.Name,
	}

	if deployment.Metadata == nil {
		return status
	}

	domainConfigs := deployment.Metadata.GetDomains()
	if len(domainConfigs) == 0 {
		return status
	}

	status.DomainConfigs = domainConfigs
	status.Domains = deployment.Metadata.GetUniqueDomainNames()
	status.Exposed = true
	status.VirtualHostExists = o.nginx.VirtualHostExists(deployment.Name)

	if len(status.Domains) > 0 {
		status.Domain = status.Domains[0]
	}

	for _, d := range domainConfigs {
		if d.SSL.Enabled {
			status.SSLEnabled = true
			break
		}
	}

	for _, domainName := range status.Domains {
		if o.ssl.CertificateExists(domainName) {
			status.CertificateExists = true
			cert, err := o.ssl.GetCertificate(domainName)
			if err == nil {
				status.Certificates = append(status.Certificates, *cert)
				if status.Certificate == nil {
					status.Certificate = cert
				}
			}
		}
	}

	return status
}

func (o *Orchestrator) ListVirtualHosts() ([]nginx.VirtualHostInfo, error) {
	return o.nginx.ListVirtualHosts()
}

func (o *Orchestrator) ListCertificates() ([]models.Certificate, error) {
	return o.ssl.ListCertificates()
}

func (o *Orchestrator) GetExpiringCertificates(days int) ([]models.Certificate, error) {
	return o.ssl.GetExpiringCertificates(days)
}

type SetupResult struct {
	DeploymentName       string                   `json:"deployment_name"`
	Domain               string                   `json:"domain,omitempty"`
	Domains              []string                 `json:"domains,omitempty"`
	Success              bool                     `json:"success"`
	Skipped              bool                     `json:"skipped"`
	Message              string                   `json:"message,omitempty"`
	VirtualHostCreated   bool                     `json:"virtual_host_created"`
	NginxReloaded        bool                     `json:"nginx_reloaded"`
	CertificateRequested bool                     `json:"certificate_requested"`
	CertificateExists    bool                     `json:"certificate_exists"`
	CertificateResults   []*ssl.CertificateResult `json:"certificate_results,omitempty"`
	SSLMessage           string                   `json:"ssl_message,omitempty"`
	SSLError             string                   `json:"ssl_error,omitempty"`
}

type ProxyStatus struct {
	DeploymentName    string                `json:"deployment_name"`
	Exposed           bool                  `json:"exposed"`
	Domain            string                `json:"domain,omitempty"`
	Domains           []string              `json:"domains,omitempty"`
	DomainConfigs     []models.DomainConfig `json:"domains_config,omitempty"`
	VirtualHostExists bool                  `json:"virtual_host_exists"`
	SSLEnabled        bool                  `json:"ssl_enabled"`
	CertificateExists bool                  `json:"certificate_exists"`
	Certificate       *models.Certificate   `json:"certificate,omitempty"`
	Certificates      []models.Certificate  `json:"certificates,omitempty"`
}
