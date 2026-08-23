package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentCreateValidatesSuppliedEnvironmentFile(t *testing.T) {
	_, deploymentsPath, httpServer := setupPlanTestServer(t)
	compose := `services:
  app:
    image: nginx:alpine
    env_file: .env
`
	response, body := doJSON(t, http.MethodPost, httpServer.URL+"/api/deployments", map[string]any{
		"name":            "env-app",
		"compose_content": compose,
		"env_vars": []map[string]string{
			{"key": "APP_MODE", "value": "staging"},
		},
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, body = %#v", response.StatusCode, body)
	}
	for _, name := range []string{".env", ".env.flatrun"} {
		content, err := os.ReadFile(filepath.Join(deploymentsPath, "env-app", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !strings.Contains(string(content), "APP_MODE=staging") {
			t.Fatalf("%s = %q", name, content)
		}
	}
}

func TestDeploymentCreateOmitsLegacyVariablesForMultipleDatabases(t *testing.T) {
	_, deploymentsPath, httpServer := setupPlanTestServer(t)
	response, body := doJSON(t, http.MethodPost, httpServer.URL+"/api/deployments", map[string]any{
		"name":            "multi-db-app",
		"compose_content": "services:\n  app:\n    image: nginx:alpine\n",
		"databases": []map[string]any{
			{"alias": "agent", "type": "postgresql", "mode": "external", "external_host": "agent-db.example.com", "external_port": 5432, "database_name": "agent", "username": "agent", "password": "agent-password"},
			{"alias": "gateway", "type": "postgres", "mode": "external", "external_host": "gateway-db.example.com", "external_port": 5432, "database_name": "gateway", "username": "gateway", "password": "gateway-password"},
		},
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, body = %#v", response.StatusCode, body)
	}
	content, err := os.ReadFile(filepath.Join(deploymentsPath, "multi-db-app", ".env"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, expected := range []string{"AGENT_HOST=agent-db.example.com", "GATEWAY_HOST=gateway-db.example.com"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("environment is missing %q: %s", expected, text)
		}
	}
	if strings.Contains(text, "\nDB_HOST=") || strings.HasPrefix(text, "DB_HOST=") {
		t.Fatalf("legacy database variables must be absent: %s", text)
	}
}
