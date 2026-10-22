package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/ai"
	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/database"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/files"
	"github.com/flatrun/agent/internal/networks"
	"github.com/flatrun/agent/internal/plan"
	"github.com/flatrun/agent/internal/proxy"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
)

type workspaceClient struct {
	t     *testing.T
	url   string
	token string
}

func (w workspaceClient) do(method, path, ws string, body any) (int, map[string]any) {
	w.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, w.url+"/api"+path, reader)
	if err != nil {
		w.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if w.token != "" {
		req.Header.Set("Authorization", "Bearer "+w.token)
	}
	if ws != "" {
		req.Header.Set(workspaceHeader, ws)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	var parsed map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&parsed)
	return resp.StatusCode, parsed
}

func (w workspaceClient) login(username, password string) workspaceClient {
	w.t.Helper()
	status, body := workspaceClient{t: w.t, url: w.url}.do(http.MethodPost, "/auth/login", "", map[string]string{"username": username, "password": password})
	if status != http.StatusOK {
		w.t.Fatalf("login %s = %d %v", username, status, body)
	}
	return workspaceClient{t: w.t, url: w.url, token: body["token"].(string)}
}

func itemNames(body map[string]any, key string) []string {
	items, _ := body["items"].([]any)
	names := []string{}
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			if v, ok := m[key].(string); ok {
				names = append(names, v)
			}
		}
	}
	return names
}

func setupWorkspaceTestServer(t *testing.T, existing ...string) (*Server, workspaceClient) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	tmpDir := t.TempDir()
	for _, name := range existing {
		dir := filepath.Join(tmpDir, name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services:\n  web:\n    image: nginx:alpine\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{
		DeploymentsPath: tmpDir,
		Auth:            config.AuthConfig{Enabled: true, JWTSecret: "workspace-test-secret"},
		Infrastructure:  config.InfrastructureConfig{DefaultProxyNetwork: "proxy"},
		Nginx:           config.NginxConfig{ConfigPath: filepath.Join(tmpDir, "nginx", "conf.d")},
		Cleanup:         config.CleanupConfig{Timeout: 2 * time.Minute},
		Plans:           config.PlansConfig{TTL: time.Hour, RetentionDays: 30},
	}
	configPath := filepath.Join(tmpDir, "config.yml")
	if err := config.Save(cfg, configPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLATRUN_ADMIN_PASSWORD", "admin-pass")
	authManager, err := auth.NewManager(tmpDir, &cfg.Auth, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { authManager.Close() })

	s := &Server{
		config:            cfg,
		configPath:        configPath,
		router:            gin.New(),
		manager:           docker.NewManager(tmpDir),
		databaseManager:   database.NewManager(),
		networksManager:   networks.NewManager(),
		authManager:       authManager,
		authMiddleware:    auth.NewMiddlewareWithManager(&cfg.Auth, authManager),
		proxyOrchestrator: proxy.NewOrchestrator(cfg),
		planStore:         plan.NewStore(tmpDir),
		aiSessions:        ai.NewSessionStore(tmpDir),
		filesManager:      files.NewManager(tmpDir),
	}
	s.setupRoutes()
	ts := httptest.NewServer(s.router)
	t.Cleanup(ts.Close)
	return s, workspaceClient{t: t, url: ts.URL}
}

func createDeploymentAs(c workspaceClient, ws, name string) (int, map[string]any) {
	return c.do(http.MethodPost, "/deployments", ws, map[string]any{
		"name":            name,
		"compose_content": "services:\n  web:\n    image: nginx:alpine\n",
	})
}

func TestExistingDeploymentsMoveIntoTheDefaultWorkspace(t *testing.T) {
	_, client := setupWorkspaceTestServer(t, "legacy")
	admin := client.login("admin", "admin-pass")

	status, body := admin.do(http.MethodGet, "/workspaces", "", nil)
	if status != http.StatusOK {
		t.Fatalf("list workspaces = %d %v", status, body)
	}
	slugs := itemNames(body, "slug")
	if len(slugs) != 2 || slugs[0] != "admin" || slugs[1] != "default" {
		t.Fatalf("admin workspaces = %v", slugs)
	}

	status, body = admin.do(http.MethodGet, "/deployments", "default", nil)
	if status != http.StatusOK || len(itemNames(body, "name")) != 1 {
		t.Fatalf("default deployments = %d %v", status, body)
	}
	item := body["items"].([]any)[0].(map[string]any)
	if item["name"] != "legacy" || item["workspace"] != "default" {
		t.Fatalf("legacy deployment = %v", item)
	}

	status, body = admin.do(http.MethodGet, "/deployments", "admin", nil)
	if status != http.StatusOK || len(itemNames(body, "name")) != 0 {
		t.Fatalf("personal workspace deployments = %d %v", status, body)
	}

	status, body = admin.do(http.MethodGet, "/workspaces/default/resources?type=deployment", "", nil)
	if ids := itemNames(body, "id"); status != http.StatusOK || len(ids) != 1 || ids[0] != "legacy" {
		t.Fatalf("default resources = %d %v", status, body)
	}
}

func TestWorkspaceMembershipGrantsDeploymentAccess(t *testing.T) {
	s, client := setupWorkspaceTestServer(t)
	for _, name := range []string{"alice", "bob", "carol"} {
		if _, err := s.authManager.CreateUser(name, name+"@example.com", name+"-pass", auth.RoleOperator, nil); err != nil {
			t.Fatal(err)
		}
	}
	alice := client.login("alice", "alice-pass")
	bob := client.login("bob", "bob-pass")
	carol := client.login("carol", "carol-pass")

	status, body := alice.do(http.MethodPost, "/workspaces", "", map[string]string{"name": "Acme Shop"})
	if status != http.StatusCreated || body["slug"] != "acme-shop" || body["role"] != "owner" {
		t.Fatalf("create workspace = %d %v", status, body)
	}

	if status, body := createDeploymentAs(alice, "acme-shop", "shop"); status != http.StatusCreated {
		t.Fatalf("create in workspace = %d %v", status, body)
	}
	if status, body := createDeploymentAs(alice, "", "notes"); status != http.StatusCreated {
		t.Fatalf("create without workspace = %d %v", status, body)
	}
	status, body = alice.do(http.MethodGet, "/deployments", "acme-shop", nil)
	if names := itemNames(body, "name"); status != http.StatusOK || len(names) != 1 || names[0] != "shop" {
		t.Fatalf("acme deployments = %d %v", status, body)
	}
	status, body = alice.do(http.MethodGet, "/deployments", "alice", nil)
	if names := itemNames(body, "name"); status != http.StatusOK || len(names) != 1 || names[0] != "notes" {
		t.Fatalf("personal deployments = %d %v", status, body)
	}

	if status, _ := bob.do(http.MethodGet, "/deployments/shop", "", nil); status != http.StatusForbidden {
		t.Fatalf("outsider reading shop = %d", status)
	}
	if status, _ := bob.do(http.MethodGet, "/workspaces/acme-shop", "", nil); status != http.StatusNotFound {
		t.Fatalf("outsider reading the workspace = %d", status)
	}
	if status, _ := bob.do(http.MethodGet, "/deployments", "acme-shop", nil); status != http.StatusNotFound {
		t.Fatalf("outsider scoping to the workspace = %d", status)
	}
	if status, _ := createDeploymentAs(bob, "acme-shop", "intruder"); status != http.StatusNotFound {
		t.Fatalf("outsider creating in the workspace = %d", status)
	}

	status, body = alice.do(http.MethodPost, "/workspaces/acme-shop/invitations", "", map[string]string{"invitee": "BOB@example.com", "role": "member"})
	if status != http.StatusCreated {
		t.Fatalf("invite = %d %v", status, body)
	}
	token := body["token"].(string)
	if status, _ := carol.do(http.MethodPost, "/workspaces/invitations/accept", "", map[string]string{"token": token}); status != http.StatusForbidden {
		t.Fatalf("someone else accepting = %d", status)
	}
	status, body = bob.do(http.MethodPost, "/workspaces/invitations/accept", "", map[string]string{"token": token})
	if status != http.StatusOK || body["slug"] != "acme-shop" || body["role"] != "member" {
		t.Fatalf("accept = %d %v", status, body)
	}

	if status, _ := bob.do(http.MethodGet, "/deployments/shop", "", nil); status != http.StatusOK {
		t.Fatalf("member reading shop = %d", status)
	}
	status, body = bob.do(http.MethodGet, "/deployments", "", nil)
	if names := itemNames(body, "name"); status != http.StatusOK || len(names) != 1 || names[0] != "shop" {
		t.Fatalf("member deployments = %d %v", status, body)
	}
	if status, _ := bob.do(http.MethodPost, "/workspaces/acme-shop/members", "", map[string]string{"username": "carol", "role": "member"}); status != http.StatusForbidden {
		t.Fatalf("member adding members = %d", status)
	}
	if status, _ := bob.do(http.MethodDelete, "/workspaces/acme-shop", "", nil); status != http.StatusForbidden {
		t.Fatalf("member deleting the workspace = %d", status)
	}

	status, body = alice.do(http.MethodPost, "/workspaces/acme-shop/members", "", map[string]string{"username": "carol", "role": "admin"})
	if status != http.StatusCreated {
		t.Fatalf("add admin = %d %v", status, body)
	}
	if status, _ := carol.do(http.MethodPost, "/workspaces/acme-shop/invitations", "", map[string]string{"invitee": "dave", "role": "owner"}); status != http.StatusForbidden {
		t.Fatalf("admin inviting an owner = %d", status)
	}

	status, body = alice.do(http.MethodGet, "/workspaces/acme-shop/members", "", nil)
	if names := itemNames(body, "username"); status != http.StatusOK || len(names) != 3 {
		t.Fatalf("members = %d %v", status, body)
	}

	if status, _ := alice.do(http.MethodDelete, "/workspaces/acme-shop", "", nil); status != http.StatusConflict {
		t.Fatalf("deleting a workspace that owns deployments = %d", status)
	}
	if status, _ := alice.do(http.MethodDelete, "/deployments/shop", "", nil); status != http.StatusForbidden {
		t.Fatalf("operator deleting a deployment = %d", status)
	}
	if status, _ := client.login("admin", "admin-pass").do(http.MethodDelete, "/deployments/shop", "", nil); status != http.StatusOK {
		t.Fatalf("delete shop = %d", status)
	}
	if status, body := alice.do(http.MethodDelete, "/workspaces/acme-shop", "", nil); status != http.StatusNoContent {
		t.Fatalf("delete empty workspace = %d %v", status, body)
	}
}

func TestMovingADeploymentNeedsAdminOnBothSides(t *testing.T) {
	s, client := setupWorkspaceTestServer(t)
	for _, name := range []string{"alice", "bob"} {
		if _, err := s.authManager.CreateUser(name, "", name+"-pass", auth.RoleOperator, nil); err != nil {
			t.Fatal(err)
		}
	}
	alice := client.login("alice", "alice-pass")
	bob := client.login("bob", "bob-pass")

	if status, _ := createDeploymentAs(alice, "", "api"); status != http.StatusCreated {
		t.Fatalf("create = %d", status)
	}
	alice.do(http.MethodPost, "/workspaces", "", map[string]string{"name": "Team"})
	if status, _ := bob.do(http.MethodPut, "/workspaces/bob/resources", "", map[string]string{"type": "deployment", "id": "api"}); status != http.StatusForbidden {
		t.Fatalf("moving someone else's deployment = %d", status)
	}
	status, body := alice.do(http.MethodPut, "/workspaces/team/resources", "", map[string]string{"type": "deployment", "id": "api"})
	if status != http.StatusOK {
		t.Fatalf("move = %d %v", status, body)
	}
	meta, err := os.ReadFile(filepath.Join(s.config.DeploymentsPath, "api", "service.yml"))
	if err != nil || !bytes.Contains(meta, []byte("workspace: team")) {
		t.Fatalf("service.yml = %q, %v", meta, err)
	}
}
