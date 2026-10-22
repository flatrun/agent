package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestEnvReferences_ResolveAcrossDeployments(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "db", nil)
	createTestDeployment(t, tmpDir, "api", nil)

	put := func(name string, vars map[string]string) (*http.Response, map[string]interface{}) {
		var list []map[string]string
		for k, v := range vars {
			list = append(list, map[string]string{"key": k, "value": v})
		}
		return doJSON(t, http.MethodPut, ts.URL+"/api/deployments/"+name+"/env", map[string]interface{}{"env_vars": list})
	}

	if resp, body := put("db", map[string]string{"PASSWORD": "first"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("db env = %d %v", resp.StatusCode, body)
	}
	if resp, body := put("api", map[string]string{
		"DATABASE_URL": "postgres://app:${{ db.PASSWORD }}@${{ db.FLATRUN_HOST }}/app",
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("api env = %d %v", resp.StatusCode, body)
	}

	envFile := readTestFile(t, filepath.Join(tmpDir, "api", ".env"))
	if !strings.Contains(envFile, "DATABASE_URL=postgres://app:first@db/app") {
		t.Errorf(".env should hold the resolved value: %s", envFile)
	}
	if refs := readTestFile(t, filepath.Join(tmpDir, "api", envReferencesFile)); !strings.Contains(refs, "${{ db.PASSWORD }}") {
		t.Errorf("references file should keep the template: %s", refs)
	}

	resp, body := doJSON(t, http.MethodGet, ts.URL+"/api/deployments/api/env", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get env = %d", resp.StatusCode)
	}
	vars := body["env_vars"].([]interface{})
	if v := vars[0].(map[string]interface{})["value"]; !strings.Contains(v.(string), "${{ db.PASSWORD }}") {
		t.Errorf("env_vars should show the template, got %v", v)
	}
	if r := body["resolved"].(map[string]interface{})["DATABASE_URL"]; r != "postgres://app:first@db/app" {
		t.Errorf("resolved = %v", r)
	}

	// A patch to another key keeps the reference rather than its resolved value.
	if resp, body := doJSON(t, http.MethodPatch, ts.URL+"/api/deployments/api/env/variables",
		map[string]interface{}{"set": []map[string]string{{"key": "LOG_LEVEL", "value": "debug"}}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("patch = %d %v", resp.StatusCode, body)
	}

	// Changing the referenced value flows through on the next refresh, which
	// runs before every start, restart, rebuild and redeploy.
	if resp, _ := put("db", map[string]string{"PASSWORD": "rotated"}); resp.StatusCode != http.StatusOK {
		t.Fatal("db update failed")
	}
	if err := s.refreshEnvReferences("api"); err != nil {
		t.Fatal(err)
	}
	envFile = readTestFile(t, filepath.Join(tmpDir, "api", ".env"))
	if !strings.Contains(envFile, "postgres://app:rotated@db/app") || !strings.Contains(envFile, "LOG_LEVEL=debug") {
		t.Errorf("refreshed .env = %s", envFile)
	}
}

func TestEnvReferences_InvalidReferenceIsRejected(t *testing.T) {
	_, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "api", nil)

	for _, value := range []string{"${{ ghost.KEY }}", "${{ api.LOOP }}"} {
		resp, body := doJSON(t, http.MethodPut, ts.URL+"/api/deployments/api/env", map[string]interface{}{
			"env_vars": []map[string]string{{"key": "LOOP", "value": value}},
		})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d %v, want 400", value, resp.StatusCode, body)
		}
	}
}
