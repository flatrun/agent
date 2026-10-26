package api

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/pkg/models"
)

func serviceSettingsServer(t *testing.T, meta *models.ServiceMetadata) (*Server, string, string) {
	t.Helper()
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose is not available to validate the overlay")
	}
	s, tmpDir, ts := setupPlanTestServer(t)
	if meta == nil {
		meta = &models.ServiceMetadata{Name: "shop", Type: "web"}
	}
	createTestDeployment(t, tmpDir, "shop", meta)
	return s, tmpDir, ts.URL + "/api/deployments/shop/services/web/settings"
}

func TestServiceSettingsAreSavedAndRenderedIntoTheOverlay(t *testing.T) {
	s, tmpDir, url := serviceSettingsServer(t, nil)

	resp, body := doJSON(t, http.MethodPut, url, map[string]any{
		"resources":   map[string]string{"cpus": "0.5", "memory": "512m"},
		"restart":     "unless-stopped",
		"logging":     map[string]any{"max_size": "10m", "max_files": 3},
		"environment": map[string]string{"PRICE": "$5"},
		"command":     []string{"nginx", "-g", "daemon off;"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %v", resp.StatusCode, body)
	}

	overlay, err := os.ReadFile(filepath.Join(tmpDir, "shop", models.ComposeOverlayFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"memory: 512m", "restart: unless-stopped", "max-size: 10m", "PRICE: $$5", "- daemon off;"} {
		if !strings.Contains(string(overlay), want) {
			t.Errorf("overlay is missing %q:\n%s", want, overlay)
		}
	}
	deployment, _ := s.manager.GetDeployment("shop")
	if got := deployment.Metadata.ServiceSettings["web"]; got.Resources == nil || got.Resources.Memory != "512m" || got.Environment["PRICE"] != "$5" {
		t.Fatalf("service.yml settings = %+v", got)
	}

	resp, body = doJSON(t, http.MethodGet, url, nil)
	settings, _ := body["settings"].(map[string]any)
	if resp.StatusCode != http.StatusOK || settings["restart"] != "unless-stopped" {
		t.Fatalf("GET = %d %v", resp.StatusCode, body)
	}
}

func TestClearingServiceSettingsRemovesTheOverlay(t *testing.T) {
	_, tmpDir, url := serviceSettingsServer(t, nil)
	if resp, body := doJSON(t, http.MethodPut, url, map[string]any{"restart": "always"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %v", resp.StatusCode, body)
	}
	if resp, body := doJSON(t, http.MethodPut, url, map[string]any{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %v", resp.StatusCode, body)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "shop", models.ComposeOverlayFile)); !os.IsNotExist(err) {
		t.Fatalf("an overlay with nothing in it must not stay active, stat err = %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(tmpDir, "shop", "service.yml"))
	if strings.Contains(string(data), "service_settings") {
		t.Fatalf("empty settings must not be saved:\n%s", data)
	}
}

func TestInvalidServiceSettingsAreRefused(t *testing.T) {
	_, tmpDir, url := serviceSettingsServer(t, nil)

	for name, body := range map[string]map[string]any{
		"memory":   {"resources": map[string]string{"memory": "1m"}},
		"cpus":     {"resources": map[string]string{"cpus": "lots"}},
		"restart":  {"restart": "sometimes"},
		"env name": {"environment": map[string]string{"1BAD": "x"}},
		"log":      {"logging": map[string]any{"max_files": 3}},
	} {
		if resp, parsed := doJSON(t, http.MethodPut, url, body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d: %v", name, resp.StatusCode, parsed)
		}
	}
	missing := strings.Replace(url, "/web/", "/ghost/", 1)
	if resp, parsed := doJSON(t, http.MethodPut, missing, map[string]any{"restart": "always"}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown service: status = %d: %v", resp.StatusCode, parsed)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "shop", models.ComposeOverlayFile)); !os.IsNotExist(err) {
		t.Fatal("a refused setting must not create an overlay")
	}
}

func TestComposeRejectionKeepsTheWorkingOverlay(t *testing.T) {
	s, tmpDir, url := serviceSettingsServer(t, nil)
	if resp, body := doJSON(t, http.MethodPut, url, map[string]any{"restart": "always"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %v", resp.StatusCode, body)
	}
	before, _ := os.ReadFile(filepath.Join(tmpDir, "shop", models.ComposeOverlayFile))

	s.manager.SetOverlayValidator(func(string, string) error {
		return &docker.OverlayValidationError{Output: "refused for the test"}
	})
	resp, body := doJSON(t, http.MethodPut, url, map[string]any{"restart": "no"})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body["error"].(string), "refused for the test") {
		t.Fatalf("status = %d: %v", resp.StatusCode, body)
	}
	after, _ := os.ReadFile(filepath.Join(tmpDir, "shop", models.ComposeOverlayFile))
	if string(after) != string(before) {
		t.Fatalf("overlay changed after compose refused the new one:\n%s", after)
	}
	deployment, _ := s.manager.GetDeployment("shop")
	if deployment.Metadata.ServiceSettings["web"].Restart != "always" {
		t.Fatalf("service.yml changed after compose refused the new settings: %+v", deployment.Metadata.ServiceSettings)
	}
}

func TestPlannedServiceSettingsHideTheEnvironmentUntilApplied(t *testing.T) {
	_, tmpDir, url := serviceSettingsServer(t, &models.ServiceMetadata{Name: "shop", Type: "web", RequirePlan: true})

	if resp, body := doJSON(t, http.MethodPut, url, map[string]any{"restart": "always"}); resp.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("a deployment that requires plans must refuse a direct change, status = %d: %v", resp.StatusCode, body)
	}

	resp, body := doJSON(t, http.MethodPut, url+"?plan=true", map[string]any{"environment": map[string]string{"API_TOKEN": "plan-only-value"}})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d: %v", resp.StatusCode, body)
	}
	p := planFromResponse(t, body)
	if strings.Contains(stringify(p), "plan-only-value") {
		t.Fatalf("the plan shown to reviewers must redact environment values: %v", p)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "shop", models.ComposeOverlayFile)); !os.IsNotExist(err) {
		t.Fatal("planning must not write the overlay")
	}

	planURL := strings.Split(url, "/api/")[0] + "/api/plans/" + p["id"].(string) + "/apply"
	if resp, body := doJSON(t, http.MethodPost, planURL, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply status = %d: %v", resp.StatusCode, body)
	}
	overlay, err := os.ReadFile(filepath.Join(tmpDir, "shop", models.ComposeOverlayFile))
	if err != nil || !strings.Contains(string(overlay), "API_TOKEN: plan-only-value") {
		t.Fatalf("applied overlay = %q, %v", overlay, err)
	}
}

func stringify(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
