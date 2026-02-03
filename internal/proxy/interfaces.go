package proxy

import (
	"github.com/flatrun/agent/internal/ssl"
	"github.com/flatrun/agent/pkg/models"
)

type NginxManager interface {
	CreateVirtualHost(deployment *models.Deployment) error
	UpdateVirtualHost(deployment *models.Deployment) error
	DeleteVirtualHost(deploymentName string) error
	VirtualHostExists(deploymentName string) bool
	GetVirtualHost(deploymentName string) (string, error)
	WriteVirtualHost(deploymentName string, content string) error
	TestConfig() error
	Reload() error
}

type SSLManager interface {
	ValidateDomain(domain string) error
	CertificateExists(domain string) bool
	RequestCertificate(domain string) (*ssl.CertificateResult, error)
	GetCertificate(domain string) (*models.Certificate, error)
	RenewCertificates() (*ssl.RenewalResult, error)
	ListCertificates() ([]models.Certificate, error)
	GetExpiringCertificates(days int) ([]models.Certificate, error)
}
