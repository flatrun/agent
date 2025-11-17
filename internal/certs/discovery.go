package certs

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"time"

	"github.com/flatrun/agent/pkg/models"
)

type Discovery struct {
	certsPath string
}

func NewDiscovery(deploymentsPath string) *Discovery {
	return &Discovery{
		certsPath: filepath.Join(deploymentsPath, "nginx", "certs", "live"),
	}
}

func (d *Discovery) FindCertificates() ([]models.Certificate, error) {
	var certificates []models.Certificate

	if _, err := os.Stat(d.certsPath); os.IsNotExist(err) {
		return certificates, nil
	}

	entries, err := os.ReadDir(d.certsPath)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		certPath := filepath.Join(d.certsPath, entry.Name(), "cert.pem")
		cert, err := d.parseCertificate(certPath, entry.Name())
		if err != nil {
			continue
		}

		certificates = append(certificates, *cert)
	}

	return certificates, nil
}

func (d *Discovery) parseCertificate(certPath, domain string) (*models.Certificate, error) {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, err
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
	}, nil
}
