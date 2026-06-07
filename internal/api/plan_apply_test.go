package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/networks"
	"github.com/flatrun/agent/internal/plan"
	"github.com/flatrun/agent/internal/proxy"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
)

func setupPlanTestServer(t *testing.T) (*Server, string, *httptest.Server) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	cfg := &config.Config{
		DeploymentsPath: tmpDir,
		Auth:            config.AuthConfig{Enabled: false},
		Infrastructure:  config.InfrastructureConfig{DefaultProxyNetwork: "proxy"},
		Nginx:           config.NginxConfig{ConfigPath: filepath.Join(tmpDir, "nginx", "conf.d")},
		Cleanup:         config.CleanupConfig{Timeout: 2 * time.Minute},
		Plans:           config.PlansConfig{TTL: time.Hour, RetentionDays: 30},
	}
	configPath := filepath.Join(tmpDir, "config.yml")
	if err := config.Save(cfg, configPath); err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	s := &Server{
		config:            cfg,
		configPath:        configPath,
		router:            gin.New(),
		manager:           docker.NewManager(tmpDir),
		networksManager:   networks.NewManager(),
		authMiddleware:    auth.NewMiddleware(&cfg.Auth),
		proxyOrchestrator: proxy.NewOrchestrator(cfg),
		planStore:         plan.NewStore(tmpDir),
	}
	s.setupRoutes()

	ts := httptest.NewServer(s.router)
	t.Cleanup(ts.Close)
	return s, tmpDir, ts
}

func doJSON(t *testing.T, method, url string, body interface{}) (*http.Response, map[string]interface{}) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var parsed map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&parsed)
	return resp, parsed
}

func planFromResponse(t *testing.T, parsed map[string]interface{}) map[string]interface{} {
	t.Helper()
	p, ok := parsed["plan"].(map[string]interface{})
	if !ok {
		t.Fatalf("response has no plan object: %v", parsed)
	}
	return p
}

func TestEnvPlanLifecycle(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", &models.ServiceMetadata{Name: "myapp", Type: "web"})

	envBody := map[string]interface{}{
		"env_vars": []map[string]string{{"key": "DB_HOST", "value": "db.internal"}},
	}

	resp, parsed := doJSON(t, http.MethodPut, ts.URL+"/api/deployments/myapp/env?plan=true", envBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("plan create status = %d, body %v", resp.StatusCode, parsed)
	}
	planObj := planFromResponse(t, parsed)
	planID := planObj["id"].(string)

	if planObj["status"] != "available" {
		t.Errorf("status = %v, want available", planObj["status"])
	}
	if planObj["action"] != "deployment.env.update" {
		t.Errorf("action = %v", planObj["action"])
	}

	// Plan must not mutate anything.
	if _, err := os.Stat(filepath.Join(tmpDir, "myapp", ".env.flatrun")); !os.IsNotExist(err) {
		t.Fatal("plan creation wrote the env file")
	}

	// Plan file is on disk under the resource directory.
	planPath := filepath.Join(tmpDir, ".flatrun", "plans", "deployment", "myapp", planID+".json")
	if _, err := os.Stat(planPath); err != nil {
		t.Fatalf("plan file missing: %v", err)
	}

	// Sensitive contents are redacted in responses...
	changes := planObj["changes"].([]interface{})
	first := changes[0].(map[string]interface{})
	if first["after"] != plan.RedactedPlaceholder {
		t.Errorf("env diff not redacted: %v", first["after"])
	}
	// ...but available with include_sensitive.
	resp, parsed = doJSON(t, http.MethodGet, ts.URL+"/api/plans/"+planID+"?include_sensitive=true", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get include_sensitive status = %d", resp.StatusCode)
	}
	full := planFromResponse(t, parsed)
	fullChange := full["changes"].([]interface{})[0].(map[string]interface{})
	if !strings.Contains(fullChange["after"].(string), "DB_HOST=db.internal") {
		t.Errorf("include_sensitive should expose the diff, got %v", fullChange["after"])
	}

	// Apply executes the planned mutation.
	resp, parsed = doJSON(t, http.MethodPost, ts.URL+"/api/plans/"+planID+"/apply", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply status = %d, body %v", resp.StatusCode, parsed)
	}
	content, err := os.ReadFile(filepath.Join(tmpDir, "myapp", ".env.flatrun"))
	if err != nil || !strings.Contains(string(content), "DB_HOST=db.internal") {
		t.Fatalf("env file not written by apply: %v %q", err, content)
	}
	stored, err := s.planStore.Get(planID)
	if err != nil || stored.Status != plan.StatusApplied {
		t.Fatalf("stored plan status = %v err %v, want applied", stored, err)
	}
	if stored.AppliedAt == nil || stored.AppliedBy == nil {
		t.Error("applied plan missing applied_at/applied_by")
	}

	// A second apply is rejected.
	resp, _ = doJSON(t, http.MethodPost, ts.URL+"/api/plans/"+planID+"/apply", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("second apply status = %d, want 409", resp.StatusCode)
	}
}

func TestPlanDriftMarksObsolete(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", nil)

	envBody := map[string]interface{}{"env_vars": []map[string]string{{"key": "A", "value": "1"}}}
	resp, parsed := doJSON(t, http.MethodPut, ts.URL+"/api/deployments/myapp/env?plan=true", envBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("plan create status = %d", resp.StatusCode)
	}
	planID := planFromResponse(t, parsed)["id"].(string)

	// Out-of-band change to the same file the plan read.
	if err := os.WriteFile(filepath.Join(tmpDir, "myapp", ".env.flatrun"), []byte("A=changed\n"), 0600); err != nil {
		t.Fatal(err)
	}

	resp, parsed = doJSON(t, http.MethodPost, ts.URL+"/api/plans/"+planID+"/apply", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale apply status = %d, body %v", resp.StatusCode, parsed)
	}
	if _, ok := parsed["drifted"]; !ok {
		t.Error("stale response missing drifted paths")
	}
	stored, _ := s.planStore.Get(planID)
	if stored.Status != plan.StatusObsolete {
		t.Errorf("stored status = %s, want obsolete", stored.Status)
	}

	// The env file keeps the out-of-band content; apply must not have run.
	content, _ := os.ReadFile(filepath.Join(tmpDir, "myapp", ".env.flatrun"))
	if string(content) != "A=changed\n" {
		t.Errorf("env file mutated by stale apply: %q", content)
	}
}

func TestExpiredPlanRejected(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", nil)

	s.config.Plans.TTL = -time.Minute
	envBody := map[string]interface{}{"env_vars": []map[string]string{{"key": "A", "value": "1"}}}
	resp, parsed := doJSON(t, http.MethodPut, ts.URL+"/api/deployments/myapp/env?plan=true", envBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("plan create status = %d", resp.StatusCode)
	}
	s.config.Plans.TTL = time.Hour
	planID := planFromResponse(t, parsed)["id"].(string)

	resp, _ = doJSON(t, http.MethodPost, ts.URL+"/api/plans/"+planID+"/apply", nil)
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("expired apply status = %d, want 410", resp.StatusCode)
	}
	stored, _ := s.planStore.Get(planID)
	if stored.Status != plan.StatusExpired {
		t.Errorf("stored status = %s, want expired", stored.Status)
	}
}

func TestApplyObsoletesSiblingPlans(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", nil)

	mkPlan := func(val string) string {
		body := map[string]interface{}{"env_vars": []map[string]string{{"key": "A", "value": val}}}
		resp, parsed := doJSON(t, http.MethodPut, ts.URL+"/api/deployments/myapp/env?plan=true", body)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("plan create status = %d", resp.StatusCode)
		}
		return planFromResponse(t, parsed)["id"].(string)
	}
	first := mkPlan("1")
	second := mkPlan("2")

	resp, _ := doJSON(t, http.MethodPost, ts.URL+"/api/plans/"+first+"/apply", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply status = %d", resp.StatusCode)
	}

	sibling, _ := s.planStore.Get(second)
	if sibling.Status != plan.StatusObsolete {
		t.Errorf("sibling status = %s, want obsolete", sibling.Status)
	}
}

func TestProtectedModeBlocksPlanAndApply(t *testing.T) {
	_, tmpDir, ts := setupPlanTestServer(t)

	// Plan creation on a protected deployment is blocked up front.
	createTestDeployment(t, tmpDir, "locked", &models.ServiceMetadata{
		Name:          "locked",
		ProtectedMode: &models.ProtectedModeConfig{Enabled: true, BlockedActions: []string{"update_env"}},
	})
	envBody := map[string]interface{}{"env_vars": []map[string]string{{"key": "A", "value": "1"}}}
	resp, _ := doJSON(t, http.MethodPut, ts.URL+"/api/deployments/locked/env?plan=true", envBody)
	if resp.StatusCode != http.StatusLocked {
		t.Fatalf("plan on protected deployment status = %d, want 423", resp.StatusCode)
	}

	// A plan created before protection turns on is blocked at apply.
	createTestDeployment(t, tmpDir, "open", nil)
	resp, parsed := doJSON(t, http.MethodPut, ts.URL+"/api/deployments/open/env?plan=true", envBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("plan create status = %d", resp.StatusCode)
	}
	planID := planFromResponse(t, parsed)["id"].(string)

	createTestDeployment(t, tmpDir, "open", &models.ServiceMetadata{
		Name:          "open",
		ProtectedMode: &models.ProtectedModeConfig{Enabled: true, BlockedActions: []string{"update_env"}},
	})
	resp, _ = doJSON(t, http.MethodPost, ts.URL+"/api/plans/"+planID+"/apply", nil)
	if resp.StatusCode != http.StatusLocked {
		t.Fatalf("apply on protected deployment status = %d, want 423", resp.StatusCode)
	}
}

func TestConfigPlanLifecycle(t *testing.T) {
	s, _, ts := setupPlanTestServer(t)

	body := map[string]interface{}{"value": "5m0s"}
	resp, parsed := doJSON(t, http.MethodPut, ts.URL+"/api/config/cleanup.timeout?plan=true", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("config plan status = %d, body %v", resp.StatusCode, parsed)
	}
	planObj := planFromResponse(t, parsed)
	planID := planObj["id"].(string)

	if got := planObj["resource"].(map[string]interface{}); got["type"] != "config" {
		t.Errorf("resource = %v", got)
	}
	if s.config.Cleanup.Timeout != 2*time.Minute {
		t.Fatal("config plan mutated live config")
	}

	resp, parsed = doJSON(t, http.MethodPost, ts.URL+"/api/plans/"+planID+"/apply", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("config apply status = %d, body %v", resp.StatusCode, parsed)
	}
	if s.config.Cleanup.Timeout != 5*time.Minute {
		t.Errorf("config not applied, timeout = %v", s.config.Cleanup.Timeout)
	}
	if parsed["applied"] != true {
		t.Errorf("runtime applier should have fired, got %v", parsed["applied"])
	}
}

func TestComposePlanShowsDiff(t *testing.T) {
	_, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", nil)

	newCompose := "name: myapp\nservices:\n  web:\n    image: nginx:1.27\n    networks:\n      - proxy\nnetworks:\n  proxy:\n    external: true\n"
	body := map[string]interface{}{"compose_content": newCompose}
	resp, parsed := doJSON(t, http.MethodPut, ts.URL+"/api/deployments/myapp?plan=true", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("compose plan status = %d, body %v", resp.StatusCode, parsed)
	}
	planObj := planFromResponse(t, parsed)
	changes := planObj["changes"].([]interface{})
	first := changes[0].(map[string]interface{})
	if first["id"] != "docker-compose.yml" {
		t.Errorf("change id = %v", first["id"])
	}
	if !strings.Contains(first["after"].(string), "nginx:1.27") {
		t.Errorf("compose diff missing new content")
	}
	// Compose file untouched by planning.
	content, _ := os.ReadFile(filepath.Join(tmpDir, "myapp", "docker-compose.yml"))
	if strings.Contains(string(content), "nginx:1.27") {
		t.Fatal("plan creation wrote the compose file")
	}
}

func TestPlanListAndDiscard(t *testing.T) {
	_, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", nil)

	envBody := map[string]interface{}{"env_vars": []map[string]string{{"key": "A", "value": "1"}}}
	resp, parsed := doJSON(t, http.MethodPut, ts.URL+"/api/deployments/myapp/env?plan=true", envBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatal("plan create failed")
	}
	planID := planFromResponse(t, parsed)["id"].(string)

	resp, parsed = doJSON(t, http.MethodGet, ts.URL+"/api/plans?deployment=myapp", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	plans := parsed["plans"].([]interface{})
	if len(plans) != 1 {
		t.Fatalf("list returned %d plans, want 1", len(plans))
	}

	resp, _ = doJSON(t, http.MethodDelete, ts.URL+"/api/plans/"+planID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("discard status = %d", resp.StatusCode)
	}
	resp, _ = doJSON(t, http.MethodGet, ts.URL+"/api/plans/"+planID, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("get after discard status = %d, want 404", resp.StatusCode)
	}
}

func TestDeletePlanPreviewsScope(t *testing.T) {
	_, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "victim", &models.ServiceMetadata{Name: "victim", Type: "web"})

	url := fmt.Sprintf("%s/api/deployments/victim?plan=true&delete_vhost=false", ts.URL)
	resp, parsed := doJSON(t, http.MethodDelete, url, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("delete plan status = %d, body %v", resp.StatusCode, parsed)
	}
	planObj := planFromResponse(t, parsed)
	planID := planObj["id"].(string)

	if _, err := os.Stat(filepath.Join(tmpDir, "victim")); err != nil {
		t.Fatal("plan creation deleted the deployment")
	}

	resp, parsed = doJSON(t, http.MethodPost, ts.URL+"/api/plans/"+planID+"/apply", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete apply status = %d, body %v", resp.StatusCode, parsed)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "victim")); !os.IsNotExist(err) {
		t.Fatal("deployment directory still exists after applied delete plan")
	}
}
