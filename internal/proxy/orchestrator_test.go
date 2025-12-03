package proxy

import (
	"errors"
	"testing"

	"github.com/flatrun/agent/internal/ssl"
	"github.com/flatrun/agent/pkg/models"
)

type mockNginxManager struct {
	createVirtualHostCalls   []string
	updateVirtualHostCalls   []string
	deleteVirtualHostCalls   []string
	testConfigCalls          int
	reloadCalls              int
	virtualHostExistsReturns map[string]bool
	createVirtualHostErr     error
	testConfigErr            error
	testConfigErrCount       int
	reloadErr                error
	lastSSLEnabled           bool
}

func (m *mockNginxManager) CreateVirtualHost(deployment *models.Deployment) error {
	m.createVirtualHostCalls = append(m.createVirtualHostCalls, deployment.Name)
	m.lastSSLEnabled = deployment.Metadata.SSL.Enabled
	return m.createVirtualHostErr
}

func (m *mockNginxManager) UpdateVirtualHost(deployment *models.Deployment) error {
	m.updateVirtualHostCalls = append(m.updateVirtualHostCalls, deployment.Name)
	m.lastSSLEnabled = deployment.Metadata.SSL.Enabled
	return nil
}

func (m *mockNginxManager) DeleteVirtualHost(deploymentName string) error {
	m.deleteVirtualHostCalls = append(m.deleteVirtualHostCalls, deploymentName)
	return nil
}

func (m *mockNginxManager) VirtualHostExists(deploymentName string) bool {
	if m.virtualHostExistsReturns == nil {
		return false
	}
	return m.virtualHostExistsReturns[deploymentName]
}

func (m *mockNginxManager) TestConfig() error {
	m.testConfigCalls++
	if m.testConfigErrCount > 0 && m.testConfigCalls <= m.testConfigErrCount {
		return m.testConfigErr
	}
	return nil
}

func (m *mockNginxManager) Reload() error {
	m.reloadCalls++
	return m.reloadErr
}

type mockSSLManager struct {
	validateDomainErr         error
	certificateExistsReturns  map[string]bool
	requestCertificateCalls   []string
	requestCertificateErr     error
	requestCertificateResult  *ssl.CertificateResult
}

func (m *mockSSLManager) ValidateDomain(domain string) error {
	return m.validateDomainErr
}

func (m *mockSSLManager) CertificateExists(domain string) bool {
	if m.certificateExistsReturns == nil {
		return false
	}
	return m.certificateExistsReturns[domain]
}

func (m *mockSSLManager) RequestCertificate(domain string) (*ssl.CertificateResult, error) {
	m.requestCertificateCalls = append(m.requestCertificateCalls, domain)
	if m.requestCertificateErr != nil {
		return nil, m.requestCertificateErr
	}
	if m.requestCertificateResult != nil {
		return m.requestCertificateResult, nil
	}
	return &ssl.CertificateResult{Domain: domain, Success: true, Message: "Certificate issued"}, nil
}

func (m *mockSSLManager) GetCertificate(domain string) (*models.Certificate, error) {
	return nil, nil
}

func (m *mockSSLManager) RenewCertificates() (*ssl.RenewalResult, error) {
	return &ssl.RenewalResult{Success: true}, nil
}

func (m *mockSSLManager) ListCertificates() ([]models.Certificate, error) {
	return nil, nil
}

func (m *mockSSLManager) GetExpiringCertificates(days int) ([]models.Certificate, error) {
	return nil, nil
}

type testableOrchestrator struct {
	nginx NginxManager
	ssl   SSLManager
}

func (o *testableOrchestrator) SetupDeployment(deployment *models.Deployment) (*SetupResult, error) {
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
		return nil, errors.New("domain is required for exposed deployments")
	}

	if err := o.ssl.ValidateDomain(domain); err != nil {
		return nil, errors.New("invalid domain: " + err.Error())
	}

	result.Domain = domain

	wantsSSL := deployment.Metadata.SSL.Enabled && deployment.Metadata.SSL.AutoCert
	certExists := o.ssl.CertificateExists(domain)

	if wantsSSL && !certExists {
		deployment.Metadata.SSL.Enabled = false
	}

	if err := o.nginx.CreateVirtualHost(deployment); err != nil {
		return nil, errors.New("failed to create virtual host: " + err.Error())
	}
	result.VirtualHostCreated = true

	if err := o.nginx.TestConfig(); err != nil {
		_ = o.nginx.DeleteVirtualHost(deployment.Name)
		return nil, errors.New("nginx config validation failed: " + err.Error())
	}

	if err := o.nginx.Reload(); err != nil {
		// warning only
	} else {
		result.NginxReloaded = true
	}

	if wantsSSL {
		if certExists {
			result.CertificateExists = true
		} else {
			certResult, err := o.ssl.RequestCertificate(domain)
			if err != nil {
				result.SSLError = err.Error()
			} else {
				result.CertificateRequested = true
				result.SSLMessage = certResult.Message
			}
		}

		if result.CertificateRequested || result.CertificateExists {
			deployment.Metadata.SSL.Enabled = true
			_ = o.nginx.UpdateVirtualHost(deployment)
			if err := o.nginx.TestConfig(); err != nil {
				deployment.Metadata.SSL.Enabled = false
				_ = o.nginx.UpdateVirtualHost(deployment)
			}
			_ = o.nginx.Reload()
		}
	}

	result.Success = true
	return result, nil
}

func TestSetupDeployment_NoMetadata(t *testing.T) {
	nginx := &mockNginxManager{}
	ssl := &mockSSLManager{}
	orch := &testableOrchestrator{nginx: nginx, ssl: ssl}

	deployment := &models.Deployment{Name: "test"}
	result, err := orch.SetupDeployment(deployment)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Skipped {
		t.Error("expected Skipped to be true")
	}
	if len(nginx.createVirtualHostCalls) != 0 {
		t.Error("CreateVirtualHost should not be called")
	}
}

func TestSetupDeployment_NotExposed(t *testing.T) {
	nginx := &mockNginxManager{}
	ssl := &mockSSLManager{}
	orch := &testableOrchestrator{nginx: nginx, ssl: ssl}

	deployment := &models.Deployment{
		Name: "test",
		Metadata: &models.ServiceMetadata{
			Networking: models.NetworkingConfig{Expose: false},
		},
	}
	result, err := orch.SetupDeployment(deployment)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Skipped {
		t.Error("expected Skipped to be true")
	}
}

func TestSetupDeployment_NoDomain(t *testing.T) {
	nginx := &mockNginxManager{}
	ssl := &mockSSLManager{}
	orch := &testableOrchestrator{nginx: nginx, ssl: ssl}

	deployment := &models.Deployment{
		Name: "test",
		Metadata: &models.ServiceMetadata{
			Networking: models.NetworkingConfig{Expose: true, Domain: ""},
		},
	}
	_, err := orch.SetupDeployment(deployment)

	if err == nil {
		t.Fatal("expected error for missing domain")
	}
}

func TestSetupDeployment_HTTPOnly(t *testing.T) {
	nginx := &mockNginxManager{}
	ssl := &mockSSLManager{}
	orch := &testableOrchestrator{nginx: nginx, ssl: ssl}

	deployment := &models.Deployment{
		Name: "test",
		Metadata: &models.ServiceMetadata{
			Networking: models.NetworkingConfig{Expose: true, Domain: "example.com"},
			SSL:        models.SSLConfig{Enabled: false},
		},
	}
	result, err := orch.SetupDeployment(deployment)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Error("expected Success to be true")
	}
	if !result.VirtualHostCreated {
		t.Error("expected VirtualHostCreated to be true")
	}
	if len(nginx.createVirtualHostCalls) != 1 {
		t.Errorf("expected 1 CreateVirtualHost call, got %d", len(nginx.createVirtualHostCalls))
	}
	if len(ssl.requestCertificateCalls) != 0 {
		t.Error("should not request certificate for HTTP-only")
	}
}

func TestSetupDeployment_SSLWithNewCert(t *testing.T) {
	nginx := &mockNginxManager{}
	ssl := &mockSSLManager{
		certificateExistsReturns: map[string]bool{"example.com": false},
	}
	orch := &testableOrchestrator{nginx: nginx, ssl: ssl}

	deployment := &models.Deployment{
		Name: "test",
		Metadata: &models.ServiceMetadata{
			Networking: models.NetworkingConfig{Expose: true, Domain: "example.com"},
			SSL:        models.SSLConfig{Enabled: true, AutoCert: true},
		},
	}
	result, err := orch.SetupDeployment(deployment)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Error("expected Success to be true")
	}

	if len(nginx.createVirtualHostCalls) != 1 {
		t.Errorf("expected 1 CreateVirtualHost call, got %d", len(nginx.createVirtualHostCalls))
	}

	if len(ssl.requestCertificateCalls) != 1 {
		t.Errorf("expected 1 RequestCertificate call, got %d", len(ssl.requestCertificateCalls))
	}

	if !result.CertificateRequested {
		t.Error("expected CertificateRequested to be true")
	}

	if len(nginx.updateVirtualHostCalls) != 1 {
		t.Errorf("expected 1 UpdateVirtualHost call for SSL, got %d", len(nginx.updateVirtualHostCalls))
	}
}

func TestSetupDeployment_SSLWithExistingCert(t *testing.T) {
	nginx := &mockNginxManager{}
	ssl := &mockSSLManager{
		certificateExistsReturns: map[string]bool{"example.com": true},
	}
	orch := &testableOrchestrator{nginx: nginx, ssl: ssl}

	deployment := &models.Deployment{
		Name: "test",
		Metadata: &models.ServiceMetadata{
			Networking: models.NetworkingConfig{Expose: true, Domain: "example.com"},
			SSL:        models.SSLConfig{Enabled: true, AutoCert: true},
		},
	}
	result, err := orch.SetupDeployment(deployment)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Error("expected Success to be true")
	}

	if len(ssl.requestCertificateCalls) != 0 {
		t.Error("should not request certificate when it already exists")
	}

	if !result.CertificateExists {
		t.Error("expected CertificateExists to be true")
	}
}

func TestSetupDeployment_HTTPFirstThenSSL(t *testing.T) {
	nginx := &mockNginxManager{}
	ssl := &mockSSLManager{
		certificateExistsReturns: map[string]bool{"example.com": false},
	}
	orch := &testableOrchestrator{nginx: nginx, ssl: ssl}

	deployment := &models.Deployment{
		Name: "test",
		Metadata: &models.ServiceMetadata{
			Networking: models.NetworkingConfig{Expose: true, Domain: "example.com"},
			SSL:        models.SSLConfig{Enabled: true, AutoCert: true},
		},
	}

	originalSSLEnabled := deployment.Metadata.SSL.Enabled
	result, err := orch.SetupDeployment(deployment)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !originalSSLEnabled {
		t.Error("original SSL.Enabled should be true")
	}

	if len(nginx.createVirtualHostCalls) != 1 {
		t.Errorf("expected 1 CreateVirtualHost call, got %d", len(nginx.createVirtualHostCalls))
	}

	if len(nginx.updateVirtualHostCalls) != 1 {
		t.Errorf("expected 1 UpdateVirtualHost call (for SSL update), got %d", len(nginx.updateVirtualHostCalls))
	}

	if !result.CertificateRequested {
		t.Error("expected CertificateRequested to be true")
	}

	if nginx.reloadCalls < 2 {
		t.Errorf("expected at least 2 reload calls (HTTP then SSL), got %d", nginx.reloadCalls)
	}
}

func TestSetupDeployment_CertRequestFails(t *testing.T) {
	nginx := &mockNginxManager{}
	ssl := &mockSSLManager{
		certificateExistsReturns: map[string]bool{"example.com": false},
		requestCertificateErr:    errors.New("certbot failed"),
	}
	orch := &testableOrchestrator{nginx: nginx, ssl: ssl}

	deployment := &models.Deployment{
		Name: "test",
		Metadata: &models.ServiceMetadata{
			Networking: models.NetworkingConfig{Expose: true, Domain: "example.com"},
			SSL:        models.SSLConfig{Enabled: true, AutoCert: true},
		},
	}
	result, err := orch.SetupDeployment(deployment)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Success {
		t.Error("expected Success to be true (HTTP should still work)")
	}

	if result.SSLError == "" {
		t.Error("expected SSLError to be set")
	}

	if len(nginx.updateVirtualHostCalls) != 0 {
		t.Error("should not update to SSL when cert request failed")
	}
}

func TestSetupDeployment_NginxConfigTestFails(t *testing.T) {
	nginx := &mockNginxManager{
		testConfigErr:      errors.New("nginx config invalid"),
		testConfigErrCount: 1,
	}
	ssl := &mockSSLManager{}
	orch := &testableOrchestrator{nginx: nginx, ssl: ssl}

	deployment := &models.Deployment{
		Name: "test",
		Metadata: &models.ServiceMetadata{
			Networking: models.NetworkingConfig{Expose: true, Domain: "example.com"},
		},
	}
	_, err := orch.SetupDeployment(deployment)

	if err == nil {
		t.Fatal("expected error when nginx config test fails")
	}

	if len(nginx.deleteVirtualHostCalls) != 1 {
		t.Error("should delete virtual host on config test failure")
	}
}
