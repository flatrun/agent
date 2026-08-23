package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestDeploymentFileOwnershipUpdatesThroughHTTP(t *testing.T) {
	_, deploymentsPath, httpServer := setupPlanTestServer(t)
	target := filepath.Join(deploymentsPath, "owned-app", "data")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	response, body := doJSON(t, http.MethodPut, httpServer.URL+"/api/deployments/owned-app/permissions/data", map[string]any{
		"uid": os.Geteuid(), "gid": os.Getegid(), "recursive": true,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %#v", response.StatusCode, body)
	}

	response, body = doJSON(t, http.MethodPut, httpServer.URL+"/api/deployments/owned-app/permissions/", map[string]any{
		"uid": os.Geteuid(), "gid": os.Getegid(),
	})
	if response.StatusCode != http.StatusInternalServerError || body["error"] != "deployment root ownership cannot be changed" {
		t.Fatalf("root status = %d, body = %#v", response.StatusCode, body)
	}
}
