package api

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/flatrun/agent/pkg/models"
)

// proxyTuningServer serves the real routes. Without a proxy (withProxy
// false) saving skips nginx, as on an agent with no proxy configured;
// planning needs the proxy to render its preview.
func proxyTuningServer(t *testing.T, withProxy bool) (string, string) {
	t.Helper()
	s, tmpDir, ts := setupPlanTestServer(t)
	if !withProxy {
		s.proxyOrchestrator = nil
	}
	createTestDeployment(t, tmpDir, "shop", &models.ServiceMetadata{
		Name:    "shop",
		Type:    "web",
		Domains: []models.DomainConfig{{ID: "site", Service: "web", ContainerPort: 80, Domain: "shop.example.com"}},
	})
	return tmpDir, ts.URL + "/api/deployments/shop/domains/site"
}

func storedDomain(t *testing.T, tmpDir string) models.DomainConfig {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(tmpDir, "shop", "service.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var meta models.ServiceMetadata
	if err := yaml.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	return meta.Domains[0]
}

func filesContaining(t *testing.T, root, needle string) []string {
	t.Helper()
	var hits []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), needle) {
			hits = append(hits, path)
		}
		return nil
	})
	return hits
}

func TestBasicAuthPasswordIsStoredOnlyAsAHash(t *testing.T) {
	tmpDir, url := proxyTuningServer(t, false)

	resp, body := doJSON(t, http.MethodPatch, url, map[string]any{
		"basic_auth": map[string]any{"enabled": true, "users": []map[string]string{{"username": "alice", "password": "s3cret-pass"}}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %v", resp.StatusCode, body)
	}
	users := storedDomain(t, tmpDir).BasicAuth.Users
	if len(users) != 1 || !strings.HasPrefix(users[0].PasswordHash, "$6$") {
		t.Fatalf("stored users = %+v", users)
	}
	if hits := filesContaining(t, tmpDir, "s3cret-pass"); len(hits) > 0 {
		t.Fatalf("the plain password was written to %v", hits)
	}

	hash := users[0].PasswordHash
	resp, body = doJSON(t, http.MethodPatch, url, map[string]any{
		"basic_auth": map[string]any{"enabled": true, "users": []map[string]string{{"username": "alice"}, {"username": "bob", "password": "other-pass"}}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %v", resp.StatusCode, body)
	}
	users = storedDomain(t, tmpDir).BasicAuth.Users
	if users[0].PasswordHash != hash {
		t.Fatalf("alice was resent without a password and must keep her hash, got %+v", users[0])
	}
	if users[1].Username != "bob" || !strings.HasPrefix(users[1].PasswordHash, "$6$") {
		t.Fatalf("bob = %+v", users[1])
	}
}

func TestPlannedBasicAuthChangeNeverStoresThePassword(t *testing.T) {
	tmpDir, url := proxyTuningServer(t, true)

	resp, body := doJSON(t, http.MethodPatch, url+"?plan=true", map[string]any{
		"basic_auth": map[string]any{"enabled": true, "users": []map[string]string{{"username": "alice", "password": "planned-pass"}}},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d: %v", resp.StatusCode, body)
	}
	if hits := filesContaining(t, tmpDir, "planned-pass"); len(hits) > 0 {
		t.Fatalf("the plain password was written to %v", hits)
	}
}

func TestInvalidProxySettingsAreRefusedBeforeSaving(t *testing.T) {
	tmpDir, url := proxyTuningServer(t, false)

	for name, patch := range map[string]map[string]any{
		"header name":       {"response_headers": map[string]string{"X Bad": "1"}},
		"header variable":   {"response_headers": map[string]string{"X-Leak": "$remote_addr"}},
		"reserved header":   {"response_headers": map[string]string{"Content-Length": "1"}},
		"address":           {"allow_ips": []string{"10.0.0.0/33"}},
		"redirect":          {"canonical_redirect": "www"},
		"auth without user": {"basic_auth": map[string]any{"enabled": true}},
		"user without pass": {"basic_auth": map[string]any{"enabled": true, "users": []map[string]string{{"username": "alice"}}}},
		"foreign hash":      {"basic_auth": map[string]any{"enabled": true, "users": []map[string]string{{"username": "alice", "password_hash": "$2y$10$abcdefghijklmnopqrstuv"}}}},
	} {
		resp, body := doJSON(t, http.MethodPatch, url, patch)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d: %v", name, resp.StatusCode, body)
		}
	}
	got := storedDomain(t, tmpDir)
	if got.ResponseHeaders != nil || got.AllowIPs != nil || got.CanonicalRedirect != "" || got.BasicAuth != nil {
		t.Fatalf("a refused setting was saved: %+v", got)
	}
}

func TestProxySettingsPersist(t *testing.T) {
	tmpDir, url := proxyTuningServer(t, false)

	resp, body := doJSON(t, http.MethodPatch, url, map[string]any{
		"proxy_buffering":    false,
		"response_headers":   map[string]string{"Cache-Control": "no-store"},
		"canonical_redirect": "primary",
		"allow_ips":          []string{" 192.0.2.0/24 ", "2001:db8::1"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %v", resp.StatusCode, body)
	}
	got := storedDomain(t, tmpDir)
	if got.ProxyBuffering == nil || *got.ProxyBuffering || got.ResponseHeaders["Cache-Control"] != "no-store" ||
		got.CanonicalRedirect != "primary" || strings.Join(got.AllowIPs, ",") != "192.0.2.0/24,2001:db8::1" {
		t.Fatalf("stored domain = %+v", got)
	}
}
