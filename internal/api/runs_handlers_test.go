package api

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	dockerclient "github.com/docker/docker/client"
	"github.com/flatrun/agent/internal/audit"
	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/runs"
	"github.com/flatrun/agent/internal/runtimes"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func setupRunsTest(t *testing.T) (*Server, workspaceClient, *runtimes.Docker) {
	t.Helper()
	if testing.Short() {
		t.Skip("requires Docker")
	}
	d, err := runtimes.NewDocker()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = d.Available(ctx); err != nil {
		t.Skipf("Docker unreachable: %v", err)
	}
	d.Scope = uuid.NewString()
	s, client := setupWorkspaceTestServer(t)
	m, err := runs.NewManager(s.config.DeploymentsPath, d, 1)
	if err != nil {
		t.Fatal(err)
	}
	s.runsManager = m
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		ids, err := d.Leftovers(ctx)
		if err != nil || len(ids) > 0 {
			t.Errorf("leftovers: %v %v", ids, err)
		}
	})
	return s, client, d
}

func submitRun(t *testing.T, client workspaceClient, body map[string]any) string {
	t.Helper()
	status, r := client.do("POST", "/runs", "", body)
	if status != http.StatusAccepted {
		t.Fatalf("submit = %d %v", status, r)
	}
	return r["id"].(string)
}

func awaitRun(t *testing.T, client workspaceClient, id, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		status, r := client.do("GET", "/runs/"+id, "", nil)
		if status != http.StatusOK {
			t.Fatalf("get = %d %v", status, r)
		}
		if r["status"] == want {
			return r
		}
		if runs.Status(r["status"].(string)).Finished() {
			t.Fatalf("wanted %s: %v", want, r)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run never reached %s", want)
	return nil
}

func runBytes(t *testing.T, client workspaceClient, path string) (int, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", client.url+"/api"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+client.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

func TestRunsHTTPCompletionArtifactsLogsAndSecrets(t *testing.T) {
	s, client, _ := setupRunsTest(t)
	am, err := audit.NewManager(s.config.DeploymentsPath, &audit.Config{Enabled: true, CaptureRequestBody: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { am.Close() })
	s.auditMiddleware = audit.NewMiddleware(am)
	s.router = gin.New()
	s.setupRoutes()
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	client.url = ts.URL
	admin := client.login("admin", "admin-pass")
	secret := "run-private-value-9417"
	id := submitRun(t, admin, map[string]any{"image": "busybox:latest", "command": []string{"sh", "-c", `test -n "$TOKEN" && test "${#TOKEN}" -eq 22 && echo hello && echo stderr >&2 && echo content > /report.txt`}, "secret_env": map[string]string{"TOKEN": secret}, "artifact_paths": []string{"/report.txt", "/missing"}})
	r := awaitRun(t, admin, id, "succeeded")
	if r["exit_code"] != float64(0) {
		t.Fatalf("exit code: %v", r)
	}
	status, body := runBytes(t, admin, "/runs/"+id+"/logs?follow=true")
	if status != http.StatusOK || !bytes.Contains(body, []byte("hello")) || !bytes.Contains(body, []byte("stderr")) {
		t.Fatalf("logs = %d %s", status, body)
	}
	status, body = runBytes(t, admin, "/runs/"+id+"/artifacts/report.txt.tar")
	if status != http.StatusOK {
		t.Fatalf("artifact = %d %s", status, body)
	}
	tr := tar.NewReader(bytes.NewReader(body))
	h, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(tr)
	if err != nil || h.Name != "report.txt" || string(body) != "content\n" {
		t.Fatalf("tar: %v %q %v", h, body, err)
	}
	raw, _ := json.Marshal(r)
	if bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte(`"secret_env"`)) {
		t.Fatalf("secret input persisted: %s", raw)
	}
	stored, err := s.runsManager.Store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(stored)
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("stored record contains secret")
	}
	for _, p := range []string{s.runsManager.LogPath(id), filepath.Join(s.config.DeploymentsPath, ".flatrun", "runs.db"), filepath.Join(s.config.DeploymentsPath, ".flatrun", "runs.db-wal")} {
		raw, err = os.ReadFile(p)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("secret found in %s", p)
		}
	}
	events, _, err := am.GetEvents(&audit.AuditFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, event := range events {
		if event.Path == "/api/runs" && event.Method == "POST" {
			seen = true
			if strings.Contains(event.RequestBody, secret) {
				t.Fatal("audit contains secret")
			}
			if event.RequestBody != "" {
				t.Fatalf("audit body: %s", event.RequestBody)
			}
		}
	}
	if !seen {
		t.Fatal("run submission not audited")
	}
}

func TestRunsHTTPTimeoutAndCancellation(t *testing.T) {
	s, client, d := setupRunsTest(t)
	admin := client.login("admin", "admin-pass")
	id := submitRun(t, admin, map[string]any{"image": "busybox:latest", "command": []string{"sleep", "30"}, "timeout_seconds": 1})
	awaitRun(t, admin, id, "timed_out")
	id = submitRun(t, admin, map[string]any{"image": "busybox:latest", "command": []string{"sleep", "30"}})
	awaitRun(t, admin, id, "running")
	queued := submitRun(t, admin, map[string]any{"image": "busybox:latest", "command": []string{"echo", "should never start"}})
	if code, body := admin.do("DELETE", "/runs/"+queued, "", nil); code != http.StatusOK {
		t.Fatalf("cancel queued: %d %v", code, body)
	}
	q := awaitRun(t, admin, queued, "cancelled")
	if q["started_at"] != nil {
		t.Fatalf("queued run started: %v", q)
	}
	if code, body := admin.do("DELETE", "/runs/"+id, "", nil); code != http.StatusOK {
		t.Fatalf("cancel running: %d %v", code, body)
	}
	awaitRun(t, admin, id, "cancelled")
	q = awaitRun(t, admin, queued, "cancelled")
	if q["started_at"] != nil {
		t.Fatalf("cancelled queued run later started: %v", q)
	}
	if _, err := os.Stat(filepath.Join(s.config.DeploymentsPath, ".flatrun", "runs", queued)); !os.IsNotExist(err) {
		t.Fatalf("queued run created a directory: %v", err)
	}
	ids, err := d.Leftovers(context.Background())
	if err != nil || len(ids) > 0 {
		t.Fatalf("containers remain: %v %v", ids, err)
	}
}

func TestRunsHTTPOwnershipAndKeyRoles(t *testing.T) {
	s, client, _ := setupRunsTest(t)
	users := map[string]*auth.User{}
	clients := map[string]workspaceClient{}
	for _, name := range []string{"alice", "bob", "viewer"} {
		role := auth.RoleOperator
		if name == "viewer" {
			role = auth.RoleViewer
		}
		u, err := s.authManager.CreateUser(name, "", name+"-pass", role, nil)
		if err != nil {
			t.Fatal(err)
		}
		users[name] = u
		clients[name] = client.login(name, name+"-pass")
	}
	ids := map[string]string{}
	for _, name := range []string{"alice", "bob"} {
		ids[name] = submitRun(t, clients[name], map[string]any{"image": "busybox:latest", "command": []string{"sh", "-c", "echo ok > /out"}, "artifact_paths": []string{"/out"}})
		awaitRun(t, clients[name], ids[name], "succeeded")
	}
	for _, name := range []string{"alice", "bob"} {
		client := clients[name]
		status, body := client.do("GET", "/runs", "", nil)
		if status != http.StatusOK || len(body["items"].([]any)) != 1 || body["items"].([]any)[0].(map[string]any)["id"] != ids[name] {
			t.Fatalf("list %s: %d %v", name, status, body)
		}
		other := ids["alice"]
		if name == "alice" {
			other = ids["bob"]
		}
		for _, suffix := range []string{"", "/logs", "/artifacts/out.tar"} {
			code, _ := runBytes(t, client, "/runs/"+other+suffix)
			if code != http.StatusNotFound {
				t.Fatalf("cross-owner %s: %d", suffix, code)
			}
		}
		if code, _ := client.do("DELETE", "/runs/"+other, "", nil); code != http.StatusNotFound {
			t.Fatalf("cross-owner cancel: %d", code)
		}
	}
	admin := client.login("admin", "admin-pass")
	code, body := admin.do("GET", "/runs", "", nil)
	if code != http.StatusOK || len(body["items"].([]any)) != 2 {
		t.Fatalf("admin list: %d %v", code, body)
	}
	adminUser, err := s.authManager.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []*auth.User{users["alice"], adminUser} {
		_, key, err := s.authManager.CreateAPIKey(u.ID, "viewer key", "", auth.RoleViewer, nil, nil, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		keyClient := workspaceClient{t: t, url: client.url, token: key}
		if code, _ := keyClient.do("POST", "/runs", "", map[string]string{"image": "busybox:latest"}); code != http.StatusForbidden {
			t.Fatalf("viewer key create: %d", code)
		}
		if u.ID == adminUser.ID {
			code, body := keyClient.do("GET", "/runs", "", nil)
			if code != http.StatusOK || len(body["items"].([]any)) != 0 {
				t.Fatalf("admin viewer key list: %d %v", code, body)
			}
			if code, _ := keyClient.do("GET", "/runs/"+ids["alice"], "", nil); code != http.StatusNotFound {
				t.Fatalf("admin viewer key read: %d", code)
			}
		}
	}
	if code, _ := clients["viewer"].do("POST", "/runs", "", map[string]string{"image": "busybox:latest"}); code != http.StatusForbidden {
		t.Fatalf("viewer create: %d", code)
	}
}

func TestRunsUnavailable(t *testing.T) {
	_, client := setupWorkspaceTestServer(t)
	admin := client.login("admin", "admin-pass")
	for _, route := range []struct{ method, path string }{{"GET", "/runs"}, {"POST", "/runs"}, {"GET", "/runs/missing"}, {"DELETE", "/runs/missing"}, {"GET", "/runs/missing/logs"}, {"GET", "/runs/missing/artifacts/out.tar"}} {
		code, body := admin.do(route.method, route.path, "", nil)
		if code != http.StatusServiceUnavailable || !strings.Contains(body["error"].(string), "unavailable") {
			t.Fatalf("unavailable: %d %v", code, body)
		}
	}
}

func TestRunsHTTPFailureAndLiveLogs(t *testing.T) {
	_, client, _ := setupRunsTest(t)
	admin := client.login("admin", "admin-pass")
	id := submitRun(t, admin, map[string]any{"image": "busybox:latest", "command": []string{"sh", "-c", "echo first; sleep 0.4; echo last >&2; exit 7"}})
	awaitRun(t, admin, id, "running")
	code, body := runBytes(t, admin, "/runs/"+id+"/logs?follow=true")
	if code != http.StatusOK || !bytes.Contains(body, []byte("first")) || !bytes.Contains(body, []byte("last")) {
		t.Fatalf("live logs = %d %s", code, body)
	}
	r := awaitRun(t, admin, id, "failed")
	if r["exit_code"] != float64(7) {
		t.Fatalf("nonzero exit: %v", r)
	}
	id = submitRun(t, admin, map[string]any{"image": "busybox:latest", "command": []string{"/nonexistent-executable"}})
	awaitRun(t, admin, id, "failed")
}

func TestRunsHTTPMissingImageDiagnostics(t *testing.T) {
	_, client, _ := setupRunsTest(t)
	admin := client.login("admin", "admin-pass")
	image := "busybox:flatrun-nonexistent-" + uuid.NewString()
	dockerClient, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer dockerClient.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := dockerClient.ImageInspect(ctx, image); !cerrdefs.IsNotFound(err) {
		t.Fatalf("missing image unexpectedly exists: %v", err)
	}
	id := submitRun(t, admin, map[string]any{"image": image})
	record := awaitRun(t, admin, id, "failed")
	if !strings.Contains(record["error"].(string), image) {
		t.Fatalf("missing image diagnostic: %v", record)
	}
	if _, err := dockerClient.ImageInspect(context.Background(), image); !cerrdefs.IsNotFound(err) {
		t.Fatalf("failed run downloaded an image: %v", err)
	}
}

func TestRunsHTTPListLimitAndTimeoutSeconds(t *testing.T) {
	_, client, _ := setupRunsTest(t)
	admin := client.login("admin", "admin-pass")
	var ids []string
	for range 3 {
		id := submitRun(t, admin, map[string]any{"image": "busybox:latest", "command": []string{"true"}, "timeout_seconds": 5})
		record := awaitRun(t, admin, id, "succeeded")
		spec := record["spec"].(map[string]any)
		if spec["timeout_seconds"] != float64(5) || spec["timeout"] != nil {
			t.Fatalf("timeout response: %v", spec)
		}
		ids = append(ids, id)
	}
	status, response := admin.do(http.MethodGet, "/runs?limit=2", "", nil)
	if status != http.StatusOK {
		t.Fatalf("limited list: %d %v", status, response)
	}
	records := response["items"].([]any)
	if len(records) != 2 || records[0].(map[string]any)["id"] != ids[2] || records[1].(map[string]any)["id"] != ids[1] {
		t.Fatalf("limited order: %v", records)
	}
	for _, query := range []string{"", "?limit=1000"} {
		status, response := admin.do(http.MethodGet, "/runs"+query, "", nil)
		if status != http.StatusOK || len(response["items"].([]any)) != 3 {
			t.Fatalf("list %q: %d %v", query, status, response)
		}
	}
	for _, query := range []string{"0", "-1", "invalid"} {
		if status, response := admin.do(http.MethodGet, "/runs?limit="+query, "", nil); status != http.StatusBadRequest {
			t.Fatalf("invalid limit: %d %v", status, response)
		}
	}
	for _, timeout := range []any{-1, 3601, 0.5} {
		if status, response := admin.do(http.MethodPost, "/runs", "", map[string]any{"image": "busybox:latest", "timeout_seconds": timeout}); status != http.StatusBadRequest {
			t.Fatalf("invalid timeout: %d %v", status, response)
		}
	}
}

type blockingCreateRuntime struct{}

func (blockingCreateRuntime) Kind() runtimes.Kind             { return runtimes.KindDocker }
func (blockingCreateRuntime) Available(context.Context) error { return nil }
func (blockingCreateRuntime) Create(ctx context.Context, _ runtimes.Spec) (runtimes.Instance, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func setupBlockingRunsTest(t *testing.T) (*Server, workspaceClient) {
	t.Helper()
	s, client := setupWorkspaceTestServer(t)
	manager, err := runs.NewManager(s.config.DeploymentsPath, blockingCreateRuntime{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	s.runsManager = manager
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	return s, client
}

func TestRunsHTTPSetupTimeout(t *testing.T) {
	_, client := setupBlockingRunsTest(t)
	admin := client.login("admin", "admin-pass")
	id := submitRun(t, admin, map[string]any{"image": "busybox:latest", "timeout_seconds": 1})
	record := awaitRun(t, admin, id, "timed_out")
	if record["started_at"] != nil {
		t.Fatalf("setup timeout started a container: %v", record)
	}
	if !strings.Contains(record["error"].(string), context.DeadlineExceeded.Error()) {
		t.Fatalf("setup timeout error: %v", record)
	}
}

func TestRunsHTTPDeploymentScopedAdminKey(t *testing.T) {
	s, client := setupBlockingRunsTest(t)
	admin := client.login("admin", "admin-pass")
	_, err := s.authManager.CreateUser("other", "", "other-pass", auth.RoleOperator, nil)
	if err != nil {
		t.Fatal(err)
	}
	other := client.login("other", "other-pass")
	ownID := submitRun(t, admin, map[string]any{"image": "busybox:latest"})
	otherID := submitRun(t, other, map[string]any{"image": "busybox:latest"})
	adminUser, err := s.authManager.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := s.authManager.CreateAPIKey(adminUser.ID, "scoped admin", "", auth.RoleAdmin, nil, auth.DeploymentAccess{"one": "write"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	scoped := workspaceClient{t: t, url: client.url, token: key}
	code, body := scoped.do("GET", "/runs", "", nil)
	if code != http.StatusOK || len(body["items"].([]any)) != 1 || body["items"].([]any)[0].(map[string]any)["id"] != ownID {
		t.Fatalf("scoped admin list: %d %v", code, body)
	}
	for _, suffix := range []string{"", "/logs", "/artifacts/out.tar"} {
		if code, _ := runBytes(t, scoped, "/runs/"+otherID+suffix); code != http.StatusNotFound {
			t.Fatalf("scoped admin read %s: %d", suffix, code)
		}
	}
	if code, _ := scoped.do("DELETE", "/runs/"+otherID, "", nil); code != http.StatusNotFound {
		t.Fatalf("scoped admin cancel: %d", code)
	}
	if code, body := other.do("GET", "/runs/"+otherID, "", nil); code != http.StatusOK || body["status"] != "queued" {
		t.Fatalf("other run changed: %d %v", code, body)
	}
	code, body = admin.do("GET", "/runs", "", nil)
	if code != http.StatusOK || len(body["items"].([]any)) != 2 {
		t.Fatalf("unrestricted admin list: %d %v", code, body)
	}
}
