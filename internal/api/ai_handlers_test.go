package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flatrun/agent/internal/ai"
	"github.com/flatrun/agent/pkg/models"
)

type stubProvider struct {
	lastRequest ai.Request
	response    *ai.Response
	err         error
}

func (s *stubProvider) Name() string { return "stub" }

func (s *stubProvider) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	s.lastRequest = req
	if s.err != nil {
		return nil, s.err
	}
	return s.response, nil
}

func TestAIStatusDisabled(t *testing.T) {
	_, _, ts := setupPlanTestServer(t)

	resp, parsed := doJSON(t, http.MethodGet, ts.URL+"/api/ai/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if parsed["enabled"] != false {
		t.Errorf("enabled = %v, want false", parsed["enabled"])
	}
	if _, leaked := parsed["api_key"]; leaked {
		t.Error("status response must never contain the api key")
	}
}

func TestAIAnalyzeDisabledReturns503(t *testing.T) {
	_, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", nil)

	resp, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/deployments/myapp/ai/analyze",
		map[string]interface{}{"intent": "diagnose"})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if parsed["code"] != "ai_disabled" {
		t.Errorf("code = %v, want ai_disabled", parsed["code"])
	}
}

func TestAIAnalyzeOperationRedactsSecrets(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", nil)
	envContent := "DB_PASSWORD=hunter2secret\nAPP_NAME=myapp\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "myapp", ".env.flatrun"), []byte(envContent), 0600); err != nil {
		t.Fatal(err)
	}

	stub := &stubProvider{response: &ai.Response{Content: "## Diagnosis\nDB auth failure", Model: "stub-model"}}
	s.aiProvider = stub

	body := map[string]interface{}{
		"intent": "diagnose",
		"sources": []map[string]interface{}{
			{
				"type":    "provided",
				"label":   "Failed deploy output",
				"content": "FATAL: password authentication failed for hunter2secret\nMYSQL_PASSWORD=other123secret",
			},
			{"type": "compose"},
		},
	}
	resp, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/deployments/myapp/ai/analyze", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", resp.StatusCode, parsed)
	}
	if parsed["analysis"] != "## Diagnosis\nDB auth failure" {
		t.Errorf("analysis = %v", parsed["analysis"])
	}
	if parsed["model"] != "stub-model" {
		t.Errorf("model = %v", parsed["model"])
	}
	if parsed["redactions"].(float64) < 2 {
		t.Errorf("redactions = %v, want >= 2", parsed["redactions"])
	}

	var prompt strings.Builder
	for _, m := range stub.lastRequest.Messages {
		prompt.WriteString(m.Content)
	}
	if strings.Contains(prompt.String(), "hunter2secret") {
		t.Error("env secret value leaked into the prompt")
	}
	if strings.Contains(prompt.String(), "other123secret") {
		t.Error("credential-shaped value leaked into the prompt")
	}
	if !strings.Contains(prompt.String(), "myapp") {
		t.Error("prompt missing deployment context")
	}
}

func TestAIAnalyzeReturnsValidatedSuggestions(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", nil)

	content := "## Diagnosis\nweb is crashing.\n```suggestions\n[" +
		`{"kind":"service_action","service":"web","action":"restart","title":"Restart web"},` +
		`{"kind":"service_action","service":"ghost","action":"restart","title":"Restart hallucinated service"}` +
		"]\n```"
	s.aiProvider = &stubProvider{response: &ai.Response{Content: content, Model: "stub"}}

	resp, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/deployments/myapp/ai/analyze",
		map[string]interface{}{
			"intent":  "diagnose",
			"sources": []map[string]interface{}{{"type": "provided", "content": "crash"}},
		})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", resp.StatusCode, parsed)
	}

	if strings.Contains(parsed["analysis"].(string), "suggestions") {
		t.Error("suggestions block leaked into analysis text")
	}
	actions := parsed["suggested_actions"].([]interface{})
	if len(actions) != 1 {
		t.Fatalf("got %d suggestions, want 1 (hallucinated service dropped): %v", len(actions), actions)
	}
	first := actions[0].(map[string]interface{})
	if first["service"] != "web" || first["action"] != "restart" {
		t.Errorf("suggestion = %v", first)
	}
}

func TestAIAnalyzeIncludesPlatformContext(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", &models.ServiceMetadata{
		Name: "myapp",
		Domains: []models.DomainConfig{
			{ID: "d1", Service: "web", Domain: "myapp.example.com"},
		},
	})
	s.config.Infrastructure.DefaultProxyNetwork = "proxy"
	s.config.Infrastructure.DefaultDatabaseNetwork = "database"
	s.config.AI.DocsURL = "https://flatrun.dev/docs/"

	stub := &stubProvider{response: &ai.Response{Content: "ok", Model: "stub"}}
	s.aiProvider = stub

	resp, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/deployments/myapp/ai/analyze",
		map[string]interface{}{
			"intent":  "diagnose",
			"sources": []map[string]interface{}{{"type": "provided", "content": "network proxyy not found"}},
		})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", resp.StatusCode, parsed)
	}

	var prompt strings.Builder
	for _, m := range stub.lastRequest.Messages {
		prompt.WriteString(m.Content)
	}
	for _, want := range []string{
		"FlatRun platform context",
		"Configured proxy network",
		"proxy",
		"myapp.example.com",
		"https://flatrun.dev/docs/",
	} {
		if !strings.Contains(prompt.String(), want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestAIAnalyzeProviderErrorMapsTo502(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", nil)
	s.aiProvider = &stubProvider{err: context.DeadlineExceeded}

	resp, _ := doJSON(t, http.MethodPost, ts.URL+"/api/deployments/myapp/ai/analyze",
		map[string]interface{}{
			"intent":  "diagnose",
			"sources": []map[string]interface{}{{"type": "provided", "content": "boom"}},
		})
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}

func TestAIAnalyzeSystem(t *testing.T) {
	s, _, ts := setupPlanTestServer(t)
	stub := &stubProvider{response: &ai.Response{Content: "## Diagnosis\nThe proxy network is missing.", Model: "stub"}}
	s.aiProvider = stub
	s.config.Infrastructure.Database.RootPassword = "rootpw-secret-1"

	body := map[string]interface{}{
		"intent": "diagnose",
		"sources": []map[string]interface{}{{
			"type":    "provided",
			"label":   "Start failed for myapp",
			"content": "network proxy declared as external, but could not be found\npassword=rootpw-secret-1",
		}},
	}
	resp, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/ai/analyze", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", resp.StatusCode, parsed)
	}
	if parsed["analysis"] != "## Diagnosis\nThe proxy network is missing." {
		t.Errorf("analysis = %v", parsed["analysis"])
	}

	var prompt strings.Builder
	for _, m := range stub.lastRequest.Messages {
		prompt.WriteString(m.Content)
	}
	if strings.Contains(prompt.String(), "rootpw-secret-1") {
		t.Error("agent credential leaked into the system diagnosis prompt")
	}
	if !strings.Contains(prompt.String(), "Start failed for myapp") {
		t.Error("source label missing from prompt")
	}

	// A provided source is required for system scope.
	resp, _ = doJSON(t, http.MethodPost, ts.URL+"/api/ai/analyze", map[string]interface{}{"intent": "diagnose"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing sources status = %d, want 400", resp.StatusCode)
	}

	// Unknown intents are rejected.
	resp, _ = doJSON(t, http.MethodPost, ts.URL+"/api/ai/analyze", map[string]interface{}{
		"intent":  "world-domination",
		"sources": []map[string]interface{}{{"type": "provided", "content": "x"}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown intent status = %d, want 400", resp.StatusCode)
	}
}

func TestAIConfigKeyMaskedButWritable(t *testing.T) {
	s, _, ts := setupPlanTestServer(t)

	resp, parsed := doJSON(t, http.MethodPut, ts.URL+"/api/config/ai.api_key",
		map[string]interface{}{"value": "sk-supersecret123"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set ai.api_key status = %d, body %v", resp.StatusCode, parsed)
	}
	if s.config.AI.APIKey != "sk-supersecret123" {
		t.Errorf("api key not set, got %q", s.config.AI.APIKey)
	}

	entry := parsed["entry"].(map[string]interface{})
	if entry["value"] != nil {
		t.Errorf("set response leaked value: %v", entry["value"])
	}
	if entry["sensitive"] != true {
		t.Errorf("entry not marked sensitive: %v", entry)
	}

	resp, parsed = doJSON(t, http.MethodGet, ts.URL+"/api/config/ai.api_key", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d", resp.StatusCode)
	}
	entry = parsed["entry"].(map[string]interface{})
	if entry["value"] != nil {
		t.Errorf("get leaked value: %v", entry["value"])
	}
}

func TestAIRuntimeApplierSwapsProvider(t *testing.T) {
	s, _, ts := setupPlanTestServer(t)
	if s.aiProvider != nil {
		t.Fatal("provider should start nil with ai disabled")
	}

	resp, parsed := doJSON(t, http.MethodPut, ts.URL+"/api/config/ai.enabled",
		map[string]interface{}{"value": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enable status = %d, body %v", resp.StatusCode, parsed)
	}
	if parsed["applied"] != true {
		t.Errorf("applied = %v, want true", parsed["applied"])
	}
	if s.aiProvider == nil {
		t.Fatal("provider not constructed by runtime applier")
	}

	resp, _ = doJSON(t, http.MethodPut, ts.URL+"/api/config/ai.enabled",
		map[string]interface{}{"value": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatal("disable failed")
	}
	if s.aiProvider != nil {
		t.Error("provider not torn down when disabled")
	}
}
