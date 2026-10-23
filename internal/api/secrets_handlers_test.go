package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/scheduler"
)

func readDeploymentFile(t *testing.T, s *Server, deployment, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.config.DeploymentsPath, deployment, name))
	if err != nil {
		t.Fatalf("read %s/%s: %v", deployment, name, err)
	}
	return string(raw)
}

func envLine(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimPrefix(line, key+"=")
		}
	}
	return ""
}

func secretsTestSetup(t *testing.T) (*Server, workspaceClient, workspaceClient, workspaceClient) {
	t.Helper()
	s, client := setupWorkspaceTestServer(t)
	for _, name := range []string{"alice", "bob", "carol"} {
		if _, err := s.authManager.CreateUser(name, name+"@example.com", name+"-pass", auth.RoleOperator, nil); err != nil {
			t.Fatal(err)
		}
	}
	alice := client.login("alice", "alice-pass")
	bob := client.login("bob", "bob-pass")
	carol := client.login("carol", "carol-pass")
	if status, body := alice.do(http.MethodPost, "/workspaces", "", map[string]string{"name": "Acme"}); status != http.StatusCreated {
		t.Fatalf("workspace = %d %v", status, body)
	}
	if status, body := alice.do(http.MethodPost, "/workspaces/acme/members", "", map[string]string{"username": "bob", "role": "member"}); status != http.StatusCreated {
		t.Fatalf("add bob = %d %v", status, body)
	}
	if status, body := createDeploymentAs(alice, "acme", "shop"); status != http.StatusCreated {
		t.Fatalf("create shop = %d %v", status, body)
	}
	return s, alice, bob, carol
}

func TestSecretsResolveIntoADeploymentsVariables(t *testing.T) {
	s, alice, bob, carol := secretsTestSetup(t)

	status, body := alice.do(http.MethodPut, "/secrets/DB_PASSWORD?scope=deployment:shop", "", map[string]string{"value": "correct-horse"})
	if status != http.StatusOK {
		t.Fatalf("set = %d %v", status, body)
	}
	if status, body := alice.do(http.MethodPut, "/secrets/SMTP_KEY?scope=workspace:acme", "", map[string]any{"generate": map[string]any{"length": 24, "charset": "hex"}}); status != http.StatusOK {
		t.Fatalf("generate = %d %v", status, body)
	}

	status, body = bob.do(http.MethodGet, "/secrets?scope=deployment:shop", "", nil)
	if names := itemNames(body, "name"); status != http.StatusOK || len(names) != 1 || names[0] != "DB_PASSWORD" {
		t.Fatalf("member listing = %d %v", status, body)
	}
	listing := strings.Join(itemNames(body, "value"), "")
	if listing != "" {
		t.Fatalf("listing exposes values: %v", body)
	}
	if status, _ := bob.do(http.MethodPut, "/secrets/SMTP_KEY?scope=workspace:acme", "", map[string]string{"value": "x"}); status != http.StatusForbidden {
		t.Fatalf("member writing a workspace secret = %d", status)
	}
	if status, _ := carol.do(http.MethodGet, "/secrets?scope=deployment:shop", "", nil); status != http.StatusForbidden {
		t.Fatalf("outsider listing deployment secrets = %d", status)
	}
	if status, _ := carol.do(http.MethodGet, "/secrets?scope=workspace:acme", "", nil); status != http.StatusNotFound {
		t.Fatalf("outsider listing workspace secrets = %d", status)
	}

	status, body = alice.do(http.MethodPut, "/deployments/shop/env", "", map[string]any{"env_vars": []map[string]string{
		{"key": "DATABASE_URL", "value": "postgres://shop:${{ secret.DB_PASSWORD }}@db/shop"},
		{"key": "SMTP_KEY", "value": "${{ secret.SMTP_KEY }}"},
	}})
	if status != http.StatusOK {
		t.Fatalf("env = %d %v", status, body)
	}
	env := readDeploymentFile(t, s, "shop", ".env.flatrun")
	if envLine(env, "DATABASE_URL") != "postgres://shop:correct-horse@db/shop" {
		t.Fatalf(".env.flatrun = %s", env)
	}
	if smtp := envLine(env, "SMTP_KEY"); len(smtp) != 24 {
		t.Fatalf("workspace secret did not resolve: %s", env)
	}
	if _, err := s.authManager.CreateUser("dave", "dave@example.com", "dave-pass", auth.RoleViewer, nil); err != nil {
		t.Fatal(err)
	}
	if status, body := alice.do(http.MethodPost, "/workspaces/acme/members", "", map[string]string{"username": "dave", "role": "member"}); status != http.StatusCreated {
		t.Fatalf("add dave = %d %v", status, body)
	}
	dave := alice.login("dave", "dave-pass")
	if status, body := dave.do(http.MethodGet, "/deployments/shop/env", "", nil); status != http.StatusOK || strings.Contains(fmt.Sprint(body), "correct-horse") {
		t.Fatalf("read-only env = %d %v", status, body)
	}
	if status, body := alice.do(http.MethodGet, "/deployments/shop/env", "", nil); status != http.StatusOK || body["resolved"].(map[string]any)["DATABASE_URL"] != "postgres://shop:correct-horse@db/shop" {
		t.Fatalf("writer env = %d %v", status, body)
	}
	if refs := readDeploymentFile(t, s, "shop", envReferencesFile); strings.Contains(refs, "correct-horse") {
		t.Fatalf("references file holds the value: %s", refs)
	}
	if raw := readDeploymentFile(t, s, ".flatrun", "secrets.json"); strings.Contains(raw, "correct-horse") {
		t.Fatalf("store holds the plain value: %s", raw)
	}

	status, body = alice.do(http.MethodPut, "/secrets/DB_PASSWORD?scope=deployment:shop", "", map[string]string{"value": "battery-staple"})
	if status != http.StatusOK {
		t.Fatalf("update = %d %v", status, body)
	}
	if refreshed := body["refreshed"].([]any); len(refreshed) != 1 || refreshed[0] != "shop" {
		t.Fatalf("refreshed = %v", body["refreshed"])
	}
	if got := envLine(readDeploymentFile(t, s, "shop", ".env.flatrun"), "DATABASE_URL"); got != "postgres://shop:battery-staple@db/shop" {
		t.Fatalf("after update DATABASE_URL = %q", got)
	}

	status, body = alice.do(http.MethodPost, "/secrets/DB_PASSWORD/rotate?scope=deployment:shop", "", nil)
	if status != http.StatusOK || body["secret"].(map[string]any)["version"] != float64(3) {
		t.Fatalf("rotate = %d %v", status, body)
	}
	if got := envLine(readDeploymentFile(t, s, "shop", ".env.flatrun"), "DATABASE_URL"); got == "postgres://shop:battery-staple@db/shop" || !strings.HasPrefix(got, "postgres://shop:") {
		t.Fatalf("after rotation DATABASE_URL = %q", got)
	}

	status, body = alice.do(http.MethodDelete, "/secrets/DB_PASSWORD?scope=deployment:shop", "", nil)
	if status != http.StatusConflict {
		t.Fatalf("deleting a referenced secret = %d %v", status, body)
	}

	if status, body := alice.do(http.MethodPut, "/deployments/shop/env", "", map[string]any{"env_vars": []map[string]string{
		{"key": "MISSING", "value": "${{ secret.NOPE }}"},
	}}); status == http.StatusOK || !strings.Contains(body["error"].(string), "NOPE") {
		t.Fatalf("unknown secret = %d %v", status, body)
	}
}

func TestScheduledRotationRewritesTheValue(t *testing.T) {
	s, alice, _, _ := secretsTestSetup(t)
	alice.do(http.MethodPut, "/secrets/TOKEN?scope=deployment:shop", "", map[string]string{"value": "first"})
	alice.do(http.MethodPut, "/deployments/shop/env", "", map[string]any{"env_vars": []map[string]string{{"key": "TOKEN", "value": "${{ secret.TOKEN }}"}}})

	if status, _ := alice.do(http.MethodPost, "/scheduler/tasks", "", map[string]any{
		"name": "sneaky", "type": "rotate_secret", "deployment_name": "shop", "cron_expr": "@daily", "enabled": true,
	}); status != http.StatusBadRequest {
		t.Fatalf("creating a rotation task directly = %d", status)
	}

	status, body := alice.do(http.MethodPut, "/secrets/TOKEN/rotation?scope=deployment:shop", "", map[string]any{"schedule": "@weekly", "length": 16, "charset": "alphanumeric"})
	if status != http.StatusOK {
		t.Fatalf("set rotation = %d %v", status, body)
	}
	tasks, err := s.schedulerManager.GetTasksByDeployment("shop")
	if err != nil || len(tasks) != 1 || tasks[0].Type != scheduler.TaskTypeRotateSecret || tasks[0].CronExpr != "@weekly" {
		t.Fatalf("tasks = %+v, %v", tasks, err)
	}

	if err := s.schedulerManager.RunTaskNow(tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		token := envLine(readDeploymentFile(t, s, "shop", ".env.flatrun"), "TOKEN")
		if token != "first" && len(token) == 16 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("TOKEN was not rotated: %q", token)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if status, _ := alice.do(http.MethodDelete, "/secrets/TOKEN/rotation?scope=deployment:shop", "", nil); status != http.StatusOK {
		t.Fatalf("clear rotation = %d", status)
	}
	if tasks, _ := s.schedulerManager.GetTasksByDeployment("shop"); len(tasks) != 0 {
		t.Fatalf("rotation task left behind: %+v", tasks)
	}
}

func TestBoundSecretManagerSuppliesVariables(t *testing.T) {
	s, alice, bob, carol := secretsTestSetup(t)
	doppler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/configs/config/secrets/download" || r.Header.Get("Authorization") != "Bearer dp.st.example" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ALGOLIA":"N9TOPUCTO","DATABASE":"brian@aws.dynamodb.com:9876","USER":"brian"}`))
	}))
	defer doppler.Close()

	if status, _ := bob.do(http.MethodPost, "/secret-connections", "acme", map[string]any{"name": "doppler", "provider": "doppler", "settings": map[string]string{"token": "dp.st.example", "api_url": doppler.URL}}); status != http.StatusForbidden {
		t.Fatalf("member adding a connection = %d", status)
	}
	status, body := alice.do(http.MethodPost, "/secret-connections", "acme", map[string]any{"name": "doppler", "provider": "doppler", "settings": map[string]string{"token": "dp.st.example", "api_url": doppler.URL}})
	if status != http.StatusCreated || body["workspace"] != "acme" || body["settings"].(map[string]any)["token"] != "********" {
		t.Fatalf("connection = %d %v", status, body)
	}
	connectionID := body["id"].(string)
	if raw := readDeploymentFile(t, s, ".flatrun", "credentials-store.yml"); strings.Contains(raw, "dp.st.example") {
		t.Fatalf("connection token stored in plain text: %s", raw)
	}
	status, body = carol.do(http.MethodGet, "/secret-connections", "", nil)
	if status != http.StatusOK || len(itemNames(body, "id")) != 0 {
		t.Fatalf("outsider sees connections: %d %v", status, body)
	}

	if status, body := bob.do(http.MethodPost, "/secret-bindings?scope=deployment:shop", "", map[string]any{"connection_id": connectionID}); status != http.StatusForbidden {
		t.Fatalf("member binding a connection = %d %v", status, body)
	}
	status, body = alice.do(http.MethodPost, "/secret-bindings?scope=deployment:shop", "", map[string]any{"connection_id": connectionID})
	if status != http.StatusCreated {
		t.Fatalf("binding = %d %v", status, body)
	}
	keys := body["keys"].([]any)
	if len(keys) != 3 || keys[0] != "ALGOLIA" {
		t.Fatalf("keys = %v", keys)
	}
	bindingID := body["binding"].(map[string]any)["id"].(string)

	status, body = alice.do(http.MethodPut, "/deployments/shop/env", "", map[string]any{"env_vars": []map[string]string{{"key": "DB_USER", "value": "${{ secret.USER }}"}}})
	if status != http.StatusOK {
		t.Fatalf("env = %d %v", status, body)
	}
	if got := envLine(readDeploymentFile(t, s, "shop", ".env.flatrun"), "DB_USER"); got != "brian" {
		t.Fatalf("DB_USER = %q", got)
	}

	status, body = alice.do(http.MethodPost, "/secret-bindings/"+bindingID+"/sync", "", nil)
	if status != http.StatusOK || body["refreshed"].([]any)[0] != "shop" {
		t.Fatalf("sync = %d %v", status, body)
	}
	if status, _ := carol.do(http.MethodDelete, "/secret-bindings/"+bindingID, "", nil); status != http.StatusForbidden {
		t.Fatalf("outsider removing a binding = %d", status)
	}
	if status, _ := alice.do(http.MethodDelete, "/secret-connections/"+connectionID, "", nil); status != http.StatusConflict {
		t.Fatalf("removing a connection in use = %d", status)
	}
}
