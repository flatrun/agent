package api

import (
	"net/http"
	"testing"

	"github.com/flatrun/agent/internal/auth"
)

func TestProjectsAreServedAsDeployments(t *testing.T) {
	s, client := setupWorkspaceTestServer(t)
	for _, name := range []string{"alice", "carol"} {
		if _, err := s.authManager.CreateUser(name, name+"@example.com", name+"-pass", auth.RoleOperator, nil); err != nil {
			t.Fatal(err)
		}
	}
	alice := client.login("alice", "alice-pass")
	carol := client.login("carol", "carol-pass")
	if status, body := alice.do(http.MethodPost, "/workspaces", "", map[string]string{"name": "Acme"}); status != http.StatusCreated {
		t.Fatalf("workspace = %d %v", status, body)
	}

	status, body := alice.do(http.MethodPost, "/projects", "acme", map[string]any{
		"name":            "shop",
		"compose_content": "services:\n  web:\n    image: nginx:alpine\n  db:\n    image: postgres:16-alpine\n",
	})
	if status != http.StatusCreated {
		t.Fatalf("create project = %d %v", status, body)
	}

	status, body = alice.do(http.MethodGet, "/projects", "acme", nil)
	if names := itemNames(body, "name"); status != http.StatusOK || len(names) != 1 || names[0] != "shop" {
		t.Fatalf("list projects = %d %v", status, body)
	}
	if status, body := alice.do(http.MethodGet, "/deployments/shop", "acme", nil); status != http.StatusOK {
		t.Fatalf("same project as a deployment = %d %v", status, body)
	}

	status, body = alice.do(http.MethodGet, "/projects/shop/services", "acme", nil)
	services, _ := body["services"].([]any)
	if status != http.StatusOK || len(services) != 2 {
		t.Fatalf("project services = %d %v", status, body)
	}

	if status, _ := carol.do(http.MethodGet, "/projects/shop", "", nil); status != http.StatusForbidden {
		t.Fatalf("outsider reading the project = %d", status)
	}
	if status, _ := alice.do(http.MethodGet, "/projectsx", "", nil); status != http.StatusNotFound {
		t.Fatalf("a path that only starts with projects = %d", status)
	}
}
