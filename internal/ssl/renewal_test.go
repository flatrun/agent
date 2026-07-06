package ssl

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flatrun/agent/pkg/config"
)

// writeTestCert generates a self-signed certificate and writes it into the
// layout that ssl.Manager expects: <certsPath>/<domain>/cert.pem.
func writeTestCert(t *testing.T, certsPath, domain string, notAfter time.Time) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: domain},
		Issuer:       pkix.Name{CommonName: "flatrun-test-ca"},
		NotBefore:    time.Now().Add(-24 * time.Hour),
		NotAfter:     notAfter,
		DNSNames:     []string{domain},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	domainDir := filepath.Join(certsPath, domain)
	if err := os.MkdirAll(domainDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	certPath := filepath.Join(domainDir, "cert.pem")
	f, err := os.Create(certPath)
	if err != nil {
		t.Fatalf("create cert file: %v", err)
	}
	defer f.Close()

	if err := pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("encode pem: %v", err)
	}
}

func newTestManager(t *testing.T) (*Manager, *mockExecutor, string) {
	t.Helper()
	tmpDir := t.TempDir()
	certsDir := filepath.Join(tmpDir, "live")
	if err := os.MkdirAll(certsDir, 0755); err != nil {
		t.Fatalf("mkdir certs: %v", err)
	}

	mock := &mockExecutor{}
	cfg := &config.CertbotConfig{CertsPath: certsDir}
	m := NewManager(cfg, tmpDir, mock)
	return m, mock, certsDir
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestRenewCertificate_ForceAddsForceRenewal(t *testing.T) {
	m, mock, certsDir := newTestManager(t)
	writeTestCert(t, certsDir, "example.com", time.Now().Add(10*24*time.Hour))

	result, err := m.RenewCertificate("example.com", true)
	if err != nil {
		t.Fatalf("RenewCertificate: %v", err)
	}
	if !result.Success || !result.Renewed {
		t.Errorf("expected Success and Renewed true, got %+v", result)
	}
	if len(result.RenewedDomains) != 1 || result.RenewedDomains[0] != "example.com" {
		t.Errorf("RenewedDomains = %v, want [example.com]", result.RenewedDomains)
	}

	args := mock.calls[0].args
	if !hasArg(args, "renew") || !hasArg(args, "--cert-name") || !hasArg(args, "--force-renewal") {
		t.Errorf("expected force renewal args, got: %v", args)
	}
}

func TestRenewCertificate_NoForceSkipsForceRenewalAndReportsNotDue(t *testing.T) {
	m, mock, certsDir := newTestManager(t)
	mock.out = []byte("Cert not yet due for renewal\nNo renewals were attempted.")
	writeTestCert(t, certsDir, "example.com", time.Now().Add(60*24*time.Hour))

	result, err := m.RenewCertificate("example.com", false)
	if err != nil {
		t.Fatalf("RenewCertificate: %v", err)
	}
	if !result.Success {
		t.Error("expected Success true")
	}
	if result.Renewed {
		t.Error("expected Renewed false when certbot reports the cert is not yet due")
	}
	if len(result.RenewedDomains) != 0 {
		t.Errorf("expected no RenewedDomains, got %v", result.RenewedDomains)
	}

	if hasArg(mock.calls[0].args, "--force-renewal") {
		t.Errorf("did not expect --force-renewal without force, got: %v", mock.calls[0].args)
	}
}

func TestRenewCertificate_ErrorsWhenMissing(t *testing.T) {
	m, mock, _ := newTestManager(t)

	_, err := m.RenewCertificate("missing.example.com", false)
	if err == nil {
		t.Fatal("expected error for missing certificate")
	}
	if len(mock.calls) != 0 {
		t.Errorf("expected no executor calls when cert missing, got %d", len(mock.calls))
	}
}

func TestSetAutoRenew_TogglesMarkerAndReflectsInCertificate(t *testing.T) {
	m, _, certsDir := newTestManager(t)
	writeTestCert(t, certsDir, "auto.example.com", time.Now().Add(20*24*time.Hour))

	cert, err := m.GetCertificate("auto.example.com")
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if !cert.AutoRenew {
		t.Error("new cert should default to auto_renew=true")
	}

	if err := m.SetAutoRenew("auto.example.com", false); err != nil {
		t.Fatalf("SetAutoRenew(false): %v", err)
	}

	cert, err = m.GetCertificate("auto.example.com")
	if err != nil {
		t.Fatalf("GetCertificate after disable: %v", err)
	}
	if cert.AutoRenew {
		t.Error("cert should report auto_renew=false after disable")
	}

	if err := m.SetAutoRenew("auto.example.com", true); err != nil {
		t.Fatalf("SetAutoRenew(true): %v", err)
	}

	cert, err = m.GetCertificate("auto.example.com")
	if err != nil {
		t.Fatalf("GetCertificate after re-enable: %v", err)
	}
	if !cert.AutoRenew {
		t.Error("cert should report auto_renew=true after re-enable")
	}
}

func TestSetAutoRenew_ErrorsWhenCertMissing(t *testing.T) {
	m, _, _ := newTestManager(t)

	if err := m.SetAutoRenew("nope.example.com", false); err == nil {
		t.Error("expected error for missing certificate")
	}
}

func TestGetExpiringCertificates_FiltersByThreshold(t *testing.T) {
	m, _, certsDir := newTestManager(t)
	writeTestCert(t, certsDir, "fresh.example.com", time.Now().Add(90*24*time.Hour))
	writeTestCert(t, certsDir, "soon.example.com", time.Now().Add(5*24*time.Hour))
	writeTestCert(t, certsDir, "expired.example.com", time.Now().Add(-24*time.Hour))

	expiring, err := m.GetExpiringCertificates(30)
	if err != nil {
		t.Fatalf("GetExpiringCertificates: %v", err)
	}

	got := make(map[string]bool)
	for _, c := range expiring {
		got[c.Domain] = true
	}

	if got["fresh.example.com"] {
		t.Error("fresh cert should not be reported as expiring")
	}
	if !got["soon.example.com"] {
		t.Error("soon cert should be reported as expiring")
	}
	if !got["expired.example.com"] {
		t.Error("expired cert should be reported as expiring")
	}
}

func TestRenewer_RenewsOnlyExpiringAutoRenewCerts(t *testing.T) {
	m, mock, certsDir := newTestManager(t)

	// Within threshold, auto-renew on (default) — should be renewed.
	writeTestCert(t, certsDir, "renew.example.com", time.Now().Add(10*24*time.Hour))

	// Within threshold, auto-renew off — should be skipped.
	writeTestCert(t, certsDir, "manual.example.com", time.Now().Add(10*24*time.Hour))
	if err := m.SetAutoRenew("manual.example.com", false); err != nil {
		t.Fatalf("SetAutoRenew: %v", err)
	}

	// Outside threshold — should be skipped.
	writeTestCert(t, certsDir, "fresh.example.com", time.Now().Add(120*24*time.Hour))

	renewed := make(map[string]bool)
	r := NewRenewer(m, 30, time.Hour, func(domain string) {
		renewed[domain] = true
	})
	r.Run()

	var renewCalls []string
	for _, call := range mock.calls {
		if len(call.args) > 0 && call.args[0] == "renew" {
			for i, a := range call.args {
				if a == "--cert-name" && i+1 < len(call.args) {
					renewCalls = append(renewCalls, call.args[i+1])
				}
			}
		}
	}

	if len(renewCalls) != 1 || renewCalls[0] != "renew.example.com" {
		t.Errorf("expected exactly one renewal for renew.example.com, got %v", renewCalls)
	}
	if !renewed["renew.example.com"] {
		t.Error("onRenew callback was not invoked for renew.example.com")
	}
	if renewed["manual.example.com"] {
		t.Error("onRenew should not fire for auto-renew=false cert")
	}
	if renewed["fresh.example.com"] {
		t.Error("onRenew should not fire for cert outside threshold")
	}
}

func TestRenewer_StartStop(t *testing.T) {
	m, _, _ := newTestManager(t)
	r := NewRenewer(m, 30, time.Hour, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r.Start(ctx)
	// Calling Start twice should be a no-op and not panic.
	r.Start(ctx)
	r.Stop()
	// Stop twice should be safe.
	r.Stop()
}
