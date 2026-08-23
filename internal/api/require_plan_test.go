package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/models"
)

func TestRequirePlanBlocksDirectMutation(t *testing.T) {
	_, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "guarded", &models.ServiceMetadata{
		Name:        "guarded",
		RequirePlan: true,
	})

	envBody := map[string]interface{}{"env_vars": []map[string]string{{"key": "A", "value": "1"}}}

	resp, parsed := doJSON(t, http.MethodPut, ts.URL+"/api/deployments/guarded/env", envBody)
	if resp.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("direct mutation status = %d, want 428", resp.StatusCode)
	}
	if parsed["code"] != "plan_required" {
		t.Errorf("code = %v, want plan_required", parsed["code"])
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "guarded", ".env.flatrun")); !os.IsNotExist(err) {
		t.Fatal("blocked mutation still wrote the env file")
	}

	// The plan path stays open: plan, then apply.
	resp, parsed = doJSON(t, http.MethodPut, ts.URL+"/api/deployments/guarded/env?plan=true", envBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("plan create status = %d, body %v", resp.StatusCode, parsed)
	}
	planID := planFromResponse(t, parsed)["id"].(string)

	resp, parsed = doJSON(t, http.MethodPost, ts.URL+"/api/plans/"+planID+"/apply", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply status = %d, body %v", resp.StatusCode, parsed)
	}
	content, err := os.ReadFile(filepath.Join(tmpDir, "guarded", ".env.flatrun"))
	if err != nil || !strings.Contains(string(content), "A=1") {
		t.Fatalf("apply did not write the env file: %v %q", err, content)
	}
}

func TestRequirePlanOffByDefault(t *testing.T) {
	_, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "open", nil)

	envBody := map[string]interface{}{"env_vars": []map[string]string{{"key": "A", "value": "1"}}}
	resp, _ := doJSON(t, http.MethodPut, ts.URL+"/api/deployments/open/env", envBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("direct mutation status = %d, want 200 when require_plan is off", resp.StatusCode)
	}
}

func TestDeploymentEnvironmentPatchPreservesOtherVariables(t *testing.T) {
	_, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "open", nil)
	if err := os.WriteFile(filepath.Join(tmpDir, "open", ".env"), []byte("KEEP=one\nCHANGE=old\nREMOVE=gone\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	body := map[string]interface{}{
		"set":    []map[string]string{{"key": "CHANGE", "value": "new"}, {"key": "ADD", "value": "two"}},
		"remove": []string{"REMOVE"},
	}
	resp, parsed := doJSON(t, http.MethodPatch, ts.URL+"/api/deployments/open/env/variables", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %v", resp.StatusCode, parsed)
	}
	content, err := os.ReadFile(filepath.Join(tmpDir, "open", ".env"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(content)
	for _, expected := range []string{"KEEP=one", "CHANGE=new", "ADD=two"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("environment is missing %q: %s", expected, got)
		}
	}
	if strings.Contains(got, "REMOVE=") {
		t.Fatalf("removed variable remains: %s", got)
	}
}

func TestRequirePlanToggleViaMetadata(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "app", &models.ServiceMetadata{Name: "app", Type: "web"})

	resp, parsed := doJSON(t, http.MethodPut, ts.URL+"/api/deployments/app/metadata",
		map[string]interface{}{"require_plan": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metadata update status = %d, body %v", resp.StatusCode, parsed)
	}

	deployment, err := s.manager.GetDeployment("app")
	if err != nil || deployment.Metadata == nil || !deployment.Metadata.RequirePlan {
		t.Fatalf("require_plan not persisted: %+v err %v", deployment, err)
	}

	// And the guard is live immediately.
	envBody := map[string]interface{}{"env_vars": []map[string]string{{"key": "A", "value": "1"}}}
	resp, _ = doJSON(t, http.MethodPut, ts.URL+"/api/deployments/app/env", envBody)
	if resp.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("status = %d, want 428 after enabling require_plan", resp.StatusCode)
	}
}

func TestUpdateDeploymentMetadataAcceptsTCPHealthCheck(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "database", &models.ServiceMetadata{Name: "database", Type: "postgres"})

	resp, parsed := doJSON(t, http.MethodPut, ts.URL+"/api/deployments/database/metadata",
		map[string]interface{}{
			"healthcheck": map[string]interface{}{
				"type":     "tcp",
				"service":  "postgres",
				"port":     5432,
				"interval": "30s",
			},
		})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metadata update status = %d, body %v", resp.StatusCode, parsed)
	}

	deployment, err := s.manager.GetDeployment("database")
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if deployment.Metadata.HealthCheck.Type != "tcp" || deployment.Metadata.HealthCheck.Service != "postgres" || deployment.Metadata.HealthCheck.Port != 5432 {
		t.Fatalf("health check not persisted: %+v", deployment.Metadata.HealthCheck)
	}
}

func TestUpdateDeploymentMetadataAcceptsChecksForMultipleServices(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "stack", &models.ServiceMetadata{Name: "stack", Type: "compose"})

	resp, parsed := doJSON(t, http.MethodPut, ts.URL+"/api/deployments/stack/metadata", map[string]interface{}{
		"healthchecks": []map[string]interface{}{
			{"type": "http", "service": "web", "port": 8080, "path": "/ready", "interval": "30s"},
			{"type": "tcp", "service": "postgres", "port": 5432, "interval": "30s"},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metadata update status = %d, body %v", resp.StatusCode, parsed)
	}

	deployment, err := s.manager.GetDeployment("stack")
	if err != nil || len(deployment.Metadata.HealthChecks) != 2 {
		t.Fatalf("health checks not persisted: %+v, error %v", deployment.Metadata.HealthChecks, err)
	}
}

func TestServiceActionPlan(t *testing.T) {
	_, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", nil)

	resp, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/deployments/myapp/services/web/restart?plan=true", nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("service plan status = %d, body %v", resp.StatusCode, parsed)
	}
	planObj := planFromResponse(t, parsed)
	if planObj["action"] != "deployment.service.restart" {
		t.Errorf("action = %v", planObj["action"])
	}
	change := planObj["changes"].([]interface{})[0].(map[string]interface{})
	if change["type"] != "service" || change["id"] != "web" {
		t.Errorf("change = %v", change)
	}
	actions := change["actions"].([]interface{})
	if len(actions) != 2 || actions[0] != "delete" || actions[1] != "create" {
		t.Errorf("restart should be a replace pair, got %v", actions)
	}
}

func TestServiceActionUnknownService(t *testing.T) {
	_, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", nil)

	resp, _ := doJSON(t, http.MethodPost, ts.URL+"/api/deployments/myapp/services/nope/start", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown service status = %d, want 400", resp.StatusCode)
	}
}

func TestServiceActionRespectsRequirePlan(t *testing.T) {
	_, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "guarded", &models.ServiceMetadata{
		Name:        "guarded",
		RequirePlan: true,
	})

	resp, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/deployments/guarded/services/web/stop", nil)
	if resp.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("status = %d, want 428, body %v", resp.StatusCode, parsed)
	}
}
