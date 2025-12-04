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

	if deployment.Metadata == nil || !deployment.Metadata.Networking.Expose {
		result.Skipped = true
		result.Message = "deployment not configured for exposure"
		return result, nil
	}

	domain := deployment.Metadata.Networking.Domain
	if domain == "" {
		return nil, fmt.Errorf("domain is required for exposed deployments")
	}

	if err := o.ssl.ValidateDomain(domain); err != nil {
		return nil, fmt.Errorf("invalid domain: %w", err)
	}

	result.Domain = domain

	wantsSSL := deployment.Metadata.SSL.Enabled && deployment.Metadata.SSL.AutoCert
	certExists := o.ssl.CertificateExists(domain)

	if wantsSSL && !certExists {
		deployment.Metadata.SSL.Enabled = false
	}

	if err := o.nginx.CreateVirtualHost(deployment); err != nil {
		return nil, fmt.Errorf("failed to create virtual host: %w", err)
	}
	result.VirtualHostCreated = true

	if err := o.nginx.TestConfig(); err != nil {
		_ = o.nginx.DeleteVirtualHost(deployment.Name)
		return nil, fmt.Errorf("nginx config validation failed: %w", err)
	}

	if err := o.nginx.Reload(); err != nil {
		log.Printf("warning: failed to reload nginx: %v", err)
	} else {
		result.NginxReloaded = true
	}

	if wantsSSL {
		if certExists {
			result.CertificateExists = true
		} else {
			certResult, err := o.ssl.RequestCertificate(domain)
			if err != nil {
				log.Printf("warning: failed to request certificate for %s: %v", domain, err)
				result.SSLError = err.Error()
			} else {
				result.CertificateRequested = true
				result.SSLMessage = certResult.Message
				// Verify certificate was actually created
				if o.ssl.CertificateExists(domain) {
					result.CertificateExists = true
				} else {
					log.Printf("warning: certificate request succeeded but cert not found for %s", domain)
					result.CertificateRequested = false
					result.SSLError = "certificate request succeeded but certificate file not found"
				}
			}
		}

		if result.CertificateExists {
			deployment.Metadata.SSL.Enabled = true
			if err := o.nginx.UpdateVirtualHost(deployment); err != nil {
				log.Printf("warning: failed to update virtual host with SSL: %v", err)
			}
			if err := o.nginx.TestConfig(); err != nil {
				log.Printf("warning: SSL config test failed, reverting to HTTP: %v", err)
				deployment.Metadata.SSL.Enabled = false
				_ = o.nginx.UpdateVirtualHost(deployment)
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
	if deployment.Metadata == nil || !deployment.Metadata.Networking.Expose {
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

	status.Exposed = deployment.Metadata.Networking.Expose
	status.Domain = deployment.Metadata.Networking.Domain
	status.VirtualHostExists = o.nginx.VirtualHostExists(deployment.Name)

	if deployment.Metadata.SSL.Enabled && status.Domain != "" {
		status.SSLEnabled = true
		status.CertificateExists = o.ssl.CertificateExists(status.Domain)

		if status.CertificateExists {
			cert, err := o.ssl.GetCertificate(status.Domain)
			if err == nil {
				status.Certificate = cert
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
	DeploymentName       string `json:"deployment_name"`
	Domain               string `json:"domain,omitempty"`
	Success              bool   `json:"success"`
	Skipped              bool   `json:"skipped"`
	Message              string `json:"message,omitempty"`
	VirtualHostCreated   bool   `json:"virtual_host_created"`
	NginxReloaded        bool   `json:"nginx_reloaded"`
	CertificateRequested bool   `json:"certificate_requested"`
	CertificateExists    bool   `json:"certificate_exists"`
	SSLMessage           string `json:"ssl_message,omitempty"`
	SSLError             string `json:"ssl_error,omitempty"`
}

type ProxyStatus struct {
	DeploymentName    string              `json:"deployment_name"`
	Exposed           bool                `json:"exposed"`
	Domain            string              `json:"domain,omitempty"`
	VirtualHostExists bool                `json:"virtual_host_exists"`
	SSLEnabled        bool                `json:"ssl_enabled"`
	CertificateExists bool                `json:"certificate_exists"`
	Certificate       *models.Certificate `json:"certificate,omitempty"`
}
