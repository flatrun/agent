package api

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

func setupRunsTest(t *testing.T) (*Server, workspaceClient, runtimes.LeftoverCleaner) {
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
	var runtime runtimes.Runtime = d
	if strings.HasPrefix(t.Name(), "TestFirecracker") {
		if os.Getenv("FLATRUN_FC_KERNEL") == "" || os.Getenv("FLATRUN_FC_BIN") == "" {
			t.Skip("requires FLATRUN_FC_KERNEL and FLATRUN_FC_BIN")
		}
		if _, err := os.Stat("/dev/kvm"); err != nil {
			t.Skip("requires /dev/kvm")
		}
		if os.Geteuid() != 0 {
			t.Skip("requires root")
		}
		binary := os.Getenv("FLATRUN_FC_BIN")
		init := filepath.Join(t.TempDir(), "flatrun-guest-init")
		build := exec.Command("go", "build", "-buildvcs=false", "-o", init, "../../cmd/guest-init")
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build init: %s %v", output, err)
		}
		runtime = runtimes.NewFirecracker(runtimes.FirecrackerConfig{Binary: binary, Jailer: filepath.Join(filepath.Dir(binary), "jailer"), Kernel: os.Getenv("FLATRUN_FC_KERNEL"), StateDir: t.TempDir(), ChrootBaseDir: os.Getenv("FLATRUN_FC_CHROOT_BASE"), GuestInit: init}, d, d.Scope)
		probe, cancelProbe := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelProbe()
		if err := runtime.Available(probe); err != nil {
			t.Fatal(err)
		}
	}
	managed := runtime
	if fc, ok := runtime.(*runtimes.Firecracker); ok {
		managed = &checkedFirecracker{Runtime: fc, t: t, fc: fc}
	}
	workers := 1
	if t.Name() == "TestFirecrackerHTTPEgressAndIsolation" {
		workers = 2
	}
	m, err := runs.NewManager(s.config.DeploymentsPath, managed, workers)
	if err != nil {
		t.Fatal(err)
	}
	s.runsManager = m
	if fc, ok := runtime.(*runtimes.Firecracker); ok {
		sum := sha256.Sum256([]byte(fc.Scope))
		scopeBase := filepath.Join(fc.Config.ChrootBaseDir, hex.EncodeToString(sum[:6]))
		binaryName := filepath.Base(fc.Config.Binary)
		before := cgroupInstances(t, binaryName)
		t.Cleanup(func() {
			if _, err := os.Stat(scopeBase); !os.IsNotExist(err) {
				t.Errorf("scope jail remains: %s: %v", scopeBase, err)
			}
			for id := range cgroupInstances(t, binaryName) {
				if !before[id] {
					t.Errorf("cgroup remains for instance %s", id)
				}
			}
		})
	}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		ids, err := runtime.(runtimes.LeftoverCleaner).Leftovers(ctx)
		if err != nil || len(ids) > 0 {
			t.Errorf("leftovers: %v %v", ids, err)
		}
	})
	return s, client, runtime.(runtimes.LeftoverCleaner)
}

func submitRun(t *testing.T, client workspaceClient, body map[string]any) string {
	t.Helper()
	if strings.HasPrefix(t.Name(), "TestFirecracker") {
		body["runtime"] = "firecracker"
	}
	status, r := client.do("POST", "/runs", "", body)
	if status != http.StatusAccepted {
		t.Fatalf("submit = %d %v", status, r)
	}
	return r["id"].(string)
}

func awaitRun(t *testing.T, client workspaceClient, id, want string) map[string]any {
	t.Helper()
	timeout := 8 * time.Second
	if strings.HasPrefix(t.Name(), "TestFirecracker") {
		timeout = 90 * time.Second
	}
	if strings.Contains(t.Name(), "ChromeScreenshot") {
		timeout = 10 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	var last map[string]any
	for time.Now().Before(deadline) {
		status, r := client.do("GET", "/runs/"+id, "", nil)
		if status != http.StatusOK {
			t.Fatalf("get = %d %v", status, r)
		}
		last = r
		if r["status"] == want {
			return r
		}
		if runs.Status(r["status"].(string)).Finished() {
			t.Fatalf("wanted %s: %v", want, r)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run never reached %s after %s: %v", want, timeout, last)
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
	if !waitFor(func() bool {
		events, _, err := am.GetEvents(&audit.AuditFilter{Action: "runs.create", Limit: 100})
		if err != nil {
			return false
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
		return seen
	}) {
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

func (blockingCreateRuntime) Type() runtimes.Type             { return runtimes.TypeDocker }
func (blockingCreateRuntime) Available(context.Context) error { return nil }
func (blockingCreateRuntime) Create(ctx context.Context, _ runtimes.Spec) (runtimes.Instance, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func setupBlockingRunsTest(t *testing.T, additional ...runtimes.Runtime) (*Server, workspaceClient) {
	t.Helper()
	s, client := setupWorkspaceTestServer(t)
	manager, err := runs.NewManager(s.config.DeploymentsPath, blockingCreateRuntime{}, 1, additional...)
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

func TestFirecrackerHTTPCompletionArtifactsLogsAndSecrets(t *testing.T) {
	TestRunsHTTPCompletionArtifactsLogsAndSecrets(t)
}
func TestFirecrackerHTTPTimeoutAndCancellation(t *testing.T) { TestRunsHTTPTimeoutAndCancellation(t) }
func TestFirecrackerHTTPOwnershipAndKeyRoles(t *testing.T)   { TestRunsHTTPOwnershipAndKeyRoles(t) }
func TestFirecrackerHTTPFailureAndLiveLogs(t *testing.T)     { TestRunsHTTPFailureAndLiveLogs(t) }
func TestFirecrackerHTTPListLimitAndTimeoutSeconds(t *testing.T) {
	TestRunsHTTPListLimitAndTimeoutSeconds(t)
}

func TestRunsHTTPRuntimeSelection(t *testing.T) {
	s, client := setupBlockingRunsTest(t)
	admin := client.login("admin", "admin-pass")
	for _, test := range []struct {
		kind   string
		status int
		reason string
	}{{"unknown", 400, "unknown runtime"}, {"firecracker", 503, "not configured on this node"}} {
		status, body := admin.do("POST", "/runs", "", map[string]any{"image": "busybox:latest", "runtime": test.kind})
		if status != test.status || !strings.Contains(body["error"].(string), test.reason) {
			t.Fatalf("%s: %d %v", test.kind, status, body)
		}
	}
	status, data := runBytes(t, admin, "/runs/runtimes")
	var listing struct {
		Items []runs.RuntimeStatus `json:"items"`
	}
	if err := json.Unmarshal(data, &listing); err != nil || status != 200 || len(listing.Items) != 2 {
		t.Fatalf("runtimes %d %s %v", status, data, err)
	}
	available := listing.Items
	if available[0].Type != runtimes.TypeDocker || !available[0].Available || available[1].Type != runtimes.TypeFirecracker || available[1].Available || !strings.Contains(available[1].Reason, "not configured on this node") {
		t.Fatalf("runtimes %+v", available)
	}
	id := submitRun(t, admin, map[string]any{"image": "busybox:latest", "runtime": "docker"})
	if status, body := admin.do("DELETE", "/runs/"+id, "", nil); status != 200 {
		t.Fatalf("cancel %d %v", status, body)
	}
	if _, err := s.runsManager.Store.Get(id); err != nil {
		t.Fatal(err)
	}
	anonymous := client
	anonymous.token = ""
	if status, _ := runBytes(t, anonymous, "/runs/runtimes"); status != 401 {
		t.Fatalf("anonymous listing %d", status)
	}
}

func TestFirecrackerHTTPNetworkIsolation(t *testing.T) {
	_, client, _ := setupRunsTest(t)
	admin := client.login("admin", "admin-pass")
	id := submitRun(t, admin, map[string]any{"image": "busybox:latest", "command": []string{"sh", "-c", "test \"$(ls /sys/class/net)\" = lo && ! ping -c 1 -W 1 1.1.1.1"}})
	awaitRun(t, admin, id, "succeeded")
}

func TestFirecrackerHTTPCancelPreservesArtifacts(t *testing.T) {
	_, client, _ := setupRunsTest(t)
	admin := client.login("admin", "admin-pass")
	id := submitRun(t, admin, map[string]any{"image": "busybox:latest", "command": []string{"sh", "-c", "echo ready > /report; echo ready; sleep 300"}, "artifact_paths": []string{"/report"}})
	awaitRun(t, admin, id, "running")
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, body := runBytes(t, admin, "/runs/"+id+"/logs")
		if bytes.Contains(body, []byte("ready")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("command did not become ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status, body := admin.do("DELETE", "/runs/"+id, "", nil); status != 200 {
		t.Fatalf("cancel %d %v", status, body)
	}
	awaitRun(t, admin, id, "cancelled")
	status, data := runBytes(t, admin, "/runs/"+id+"/artifacts/report.tar")
	if status != 200 {
		t.Fatalf("artifact after cancellation %d %s", status, data)
	}
	tr := tar.NewReader(bytes.NewReader(data))
	if _, err := tr.Next(); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(tr)
	if err != nil || string(body) != "ready\n" {
		t.Fatalf("artifact %q %v", body, err)
	}
}

func TestFirecrackerHTTPChromeScreenshot(t *testing.T) {
	_, client, _ := setupRunsTest(t)
	admin := client.login("admin", "admin-pass")
	id := submitRun(t, admin, map[string]any{"image": "chromedp/headless-shell:stable", "command": []string{"--headless", "--disable-gpu", "--disable-dev-shm-usage", "--screenshot=/screenshot.png", "about:blank"}, "memory_bytes": 1024 << 20, "artifact_paths": []string{"/screenshot.png"}})
	awaitRun(t, admin, id, "succeeded")
	status, data := runBytes(t, admin, "/runs/"+id+"/artifacts/screenshot.png.tar")
	if status != 200 {
		t.Fatalf("screenshot %d %s", status, data)
	}
	tr := tar.NewReader(bytes.NewReader(data))
	if _, err := tr.Next(); err != nil {
		t.Fatal(err)
	}
	signature := make([]byte, 8)
	if _, err := io.ReadFull(tr, signature); err != nil || !bytes.Equal(signature, []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		t.Fatalf("PNG signature %q %v", signature, err)
	}
}

func cgroupInstances(t *testing.T, binary string) map[string]bool {
	t.Helper()
	mounts, err := os.ReadFile("/proc/mounts")
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]bool{}
	for _, line := range strings.Split(string(mounts), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[2] != "cgroup2" {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(fields[1], binary))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				result[entry.Name()] = true
			}
		}
	}
	return result
}

func TestFirecrackerHTTPEgressAndIsolation(t *testing.T) {
	_, client, cleaner := setupRunsTest(t)
	admin := client.login("admin", "admin-pass")
	fc := cleaner.(*runtimes.Firecracker)
	peer := submitRun(t, admin, map[string]any{"image": "node:22-alpine", "egress": true, "command": []string{"node", "-e", "require('net').createServer(s=>s.end('peer')).listen(8080,'0.0.0.0',()=>console.log('peer-ready'))"}})
	awaitRun(t, admin, peer, string(runs.StatusRunning))
	ready := false
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		code, logs := runBytes(t, admin, "/runs/"+peer+"/logs")
		if code == http.StatusOK && bytes.Contains(logs, []byte("peer-ready")) {
			ready = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("peer listener never became ready")
	}

	ids, err := cleaner.Leftovers(context.Background())
	if err != nil || len(ids) != 1 {
		t.Fatalf("peer state: %v %v", ids, err)
	}
	sum := sha256.Sum256([]byte(fc.Scope))
	statePath := filepath.Join(fc.Config.StateDir, hex.EncodeToString(sum[:6]), ids[0], "state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state struct{ Subnet string }
	if err = json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	ip, _, err := net.ParseCIDR(state.Subnet)
	if err != nil {
		t.Fatal(err)
	}
	ip = ip.To4()
	ip[3] += 2
	// An accepting peer and host listener distinguish a firewall drop from a closed port.
	host, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	port := host.Addr().(*net.TCPAddr).Port
	script := fmt.Sprintf(`const net=require('net'),dns=require('dns').promises;
function connect(host,port){return new Promise(resolve=>{const s=net.connect({host,port});let done=false;function end(ok){if(done)return;done=true;s.destroy();resolve(ok)}s.setTimeout(3000,()=>end(false));s.on('connect',()=>end(true));s.on('error',()=>end(false))})}
(async()=>{await dns.lookup('one.one.one.one');if(!await connect('1.1.1.1',443))throw Error('outside unreachable');const fs=require('fs');const row=fs.readFileSync('/proc/net/route','utf8').split('\n').map(s=>s.trim().split(/\s+/)).find(r=>r[1]==='00000000');if(!row)throw Error('no default route');const gateway=row[2].match(/../g).reverse().map(x=>parseInt(x,16)).join('.');if(await connect(gateway,%d))throw Error('host reachable');if(await connect('%s',8080))throw Error('peer reachable')})().catch(e=>{console.error(e);process.exitCode=1})`, port, ip.String())
	probe := submitRun(t, admin, map[string]any{"image": "node:22-alpine", "egress": true, "command": []string{"node", "-e", script}})
	awaitRun(t, admin, probe, string(runs.StatusSucceeded))
	if code, record := admin.do("GET", "/runs/"+peer, "", nil); code != http.StatusOK || record["status"] != string(runs.StatusRunning) {
		t.Fatalf("peer stopped during probe: %d %v", code, record)
	}
	status, result := admin.do("DELETE", "/runs/"+peer, "", nil)
	if status != http.StatusOK {
		t.Fatalf("cancel: %d %v", status, result)
	}
	awaitRun(t, admin, peer, string(runs.StatusCancelled))
	for _, id := range ids {
		assertFirecrackerNetworkRemoved(t, fc.Scope, id)
	}
}

func assertFirecrackerNetworkRemoved(t *testing.T, scope, id string) {
	t.Helper()
	sum := sha256.Sum256([]byte(scope + id))
	key := hex.EncodeToString(sum[:5])
	if _, err := os.Stat("/sys/class/net/fr" + key); !os.IsNotExist(err) {
		t.Errorf("tap remains: fr%s: %v", key, err)
	}
	for _, binary := range []string{"iptables-save", "ip6tables-save"} {
		output, err := exec.Command(binary).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v: %s", binary, err, output)
		}
		if strings.Contains(string(output), "fr"+key) || strings.Contains(string(output), "FR"+key) {
			t.Errorf("network rules remain in %s: %s", binary, output)
		}
	}
}

type checkedFirecracker struct {
	runtimes.Runtime
	t      *testing.T
	fc     *runtimes.Firecracker
	mu     sync.Mutex
	active int
}

func (f *checkedFirecracker) Create(ctx context.Context, spec runtimes.Spec) (runtimes.Instance, error) {
	f.mu.Lock()
	f.active++
	f.mu.Unlock()
	instance, err := f.Runtime.Create(ctx, spec)
	if err != nil {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
		return instance, err
	}
	return &checkedFirecrackerInstance{Instance: instance, owner: f, egress: spec.Network.Egress}, nil
}

type checkedFirecrackerInstance struct {
	runtimes.Instance
	owner  *checkedFirecracker
	egress bool
}

func (i *checkedFirecrackerInstance) Destroy(ctx context.Context) error {
	err := i.Instance.Destroy(ctx)
	f := i.owner
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active--
	if i.egress {
		assertFirecrackerNetworkRemoved(f.t, f.fc.Scope, i.ID())
	}
	if cgroupInstances(f.t, filepath.Base(f.fc.Config.Binary))[i.ID()] {
		f.t.Errorf("cgroup remains: %s", i.ID())
	}
	sum := sha256.Sum256([]byte(f.fc.Scope))
	base := filepath.Join(f.fc.Config.ChrootBaseDir, hex.EncodeToString(sum[:6]))
	if _, statErr := os.Stat(filepath.Join(base, filepath.Base(f.fc.Config.Binary), i.ID())); !os.IsNotExist(statErr) {
		f.t.Errorf("instance jail remains: %s: %v", i.ID(), statErr)
	}
	if f.active == 0 {
		stateScope := filepath.Join(f.fc.Config.StateDir, hex.EncodeToString(sum[:6]))
		if _, statErr := os.Stat(stateScope); !os.IsNotExist(statErr) {
			f.t.Errorf("state scope remains: %s: %v", stateScope, statErr)
		}
		if _, statErr := os.Stat(base); !os.IsNotExist(statErr) {
			f.t.Errorf("scope jail remains: %s: %v", base, statErr)
		}
	}
	return err
}
