package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/credentials"
	"github.com/flatrun/agent/internal/webhook"
	"github.com/gin-gonic/gin"
)

// repoWithDevelopBranch returns a repository whose main and develop branches
// differ, and the develop head.
func repoWithDevelopBranch(t *testing.T) (string, string) {
	t.Helper()
	repo := gitRepoWithCompose(t)
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("checkout", "-q", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("develop"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "develop work")
	head := run("rev-parse", "HEAD")
	run("checkout", "-q", "main")
	return repo, head
}

func newEnvironmentTestServer(t *testing.T, runner *fakeRunner) (*Server, *gin.Engine, string) {
	t.Helper()
	s, r := newRedeployTestServer(t, runner)
	r.GET("/api/deployments/:name/environments", s.listEnvironments)
	r.POST("/api/deployments/:name/environments", s.createEnvironmentHandler)

	repo, developHead := repoWithDevelopBranch(t)
	create := httptest.NewRecorder()
	r.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/deployments", strings.NewReader(
		`{"name":"shop","env_vars":[{"key":"API_URL","value":"https://api.example.com"}],"source":{"type":"git","ref":"file://`+repo+`","branch":"main","auto_deploy":true}}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create base = %d: %s", create.Code, create.Body.String())
	}
	return s, r, developHead
}

func TestCreateEnvironment(t *testing.T) {
	runner := &fakeRunner{optsCh: make(chan actionOptions, 1)}
	s, r, developHead := newEnvironmentTestServer(t, runner)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/deployments/shop/environments",
		strings.NewReader(`{"name":"staging","branch":"develop"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create environment = %d: %s", rec.Code, rec.Body.String())
	}
	select {
	case opts := <-runner.optsCh:
		if opts.Revision != developHead {
			t.Errorf("environment deploy revision = %q, want develop head %q", opts.Revision, developHead)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("environment deploy never started")
	}

	env, err := s.manager.GetDeployment("shop-staging")
	if err != nil {
		t.Fatal(err)
	}
	if env.Metadata.Environment == nil || env.Metadata.Environment.Base != "shop" || env.Metadata.Environment.Name != "staging" {
		t.Errorf("environment info = %+v", env.Metadata.Environment)
	}
	if env.Metadata.Source.Branch != "develop" || env.Metadata.Source.LastRevision != developHead {
		t.Errorf("environment source = %+v", env.Metadata.Source)
	}
	if _, err := os.Stat(filepath.Join(env.Path, "feature.txt")); err != nil {
		t.Errorf("environment should hold the develop branch files: %v", err)
	}
	envFile, _ := os.ReadFile(filepath.Join(env.Path, ".env.flatrun"))
	if !strings.Contains(string(envFile), "API_URL=https://api.example.com") {
		t.Errorf("variables were not copied from the base: %q", envFile)
	}

	list := httptest.NewRecorder()
	r.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/deployments/shop/environments", nil))
	var body struct {
		Environments []struct {
			Name string `json:"name"`
		} `json:"environments"`
	}
	_ = json.Unmarshal(list.Body.Bytes(), &body)
	if len(body.Environments) != 1 || body.Environments[0].Name != "shop-staging" {
		t.Errorf("environments = %s", list.Body.String())
	}

	dup := httptest.NewRecorder()
	r.ServeHTTP(dup, httptest.NewRequest(http.MethodPost, "/api/deployments/shop/environments",
		strings.NewReader(`{"name":"staging"}`)))
	if dup.Code != http.StatusConflict {
		t.Errorf("duplicate environment = %d, want 409", dup.Code)
	}
	bad := httptest.NewRecorder()
	r.ServeHTTP(bad, httptest.NewRequest(http.MethodPost, "/api/deployments/shop/environments",
		strings.NewReader(`{"name":"Bad Name"}`)))
	if bad.Code != http.StatusBadRequest {
		t.Errorf("invalid environment name = %d, want 400", bad.Code)
	}
}

func TestPushDeploysEnvironmentTrackingThatBranch(t *testing.T) {
	runner := &fakeRunner{optsCh: make(chan actionOptions, 4)}
	s, r, _ := newEnvironmentTestServer(t, runner)
	s.credentialsManager = credentials.NewManager(t.TempDir())
	s.webhooks = webhook.DefaultRegistry()
	r.POST("/api/hooks/:provider/:deployment", s.receiveWebhook)
	r.POST("/api/deployments/:name/webhook/rotate", s.rotateDeploymentWebhook)

	create := httptest.NewRecorder()
	r.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/deployments/shop/environments",
		strings.NewReader(`{"name":"staging","branch":"develop"}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create environment = %d: %s", create.Code, create.Body.String())
	}
	<-runner.optsCh

	// Point both at the repository the captured push fixture came from; the
	// environment follows the pushed branch, the base does not.
	for name, branch := range map[string]string{"shop": "main", "shop-staging": "master"} {
		d, _ := s.manager.GetDeployment(name)
		d.Metadata.Source.Ref = "https://github.com/Codertocat/Hello-World.git"
		d.Metadata.Source.Branch = branch
		if err := s.manager.SaveMetadata(name, d.Metadata); err != nil {
			t.Fatal(err)
		}
	}
	rotate := httptest.NewRecorder()
	r.ServeHTTP(rotate, httptest.NewRequest(http.MethodPost, "/api/deployments/shop/webhook/rotate", nil))
	var secret struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(rotate.Body.Bytes(), &secret)

	rec := deliverGitHubTo(r, "shop", "push", "github-push.json", secret.Secret)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("push = %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Jobs map[string]string `json:"jobs"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Jobs) != 1 || resp.Jobs["shop-staging"] == "" {
		t.Errorf("jobs = %v, want only shop-staging", resp.Jobs)
	}
}

func TestEnvironmentIsNotCreatedWhenBaseVariablesAreUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads files regardless of their mode")
	}
	s, r, _ := newEnvironmentTestServer(t, &fakeRunner{optsCh: make(chan actionOptions, 1)})
	base, err := s.manager.GetDeployment("shop")
	if err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(base.Path, ".env")
	if err := os.Chmod(envFile, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(envFile, 0600) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/deployments/shop/environments",
		strings.NewReader(`{"name":"staging","branch":"develop"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create environment = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := s.manager.GetDeployment("shop-staging"); err == nil {
		t.Error("environment was created without the base's variables")
	}
}

func TestFailedEnvironmentIsRemovedSoItCanBeRetried(t *testing.T) {
	s, r, _ := newEnvironmentTestServer(t, &fakeRunner{optsCh: make(chan actionOptions, 1)})
	base, err := s.manager.GetDeployment("shop")
	if err != nil {
		t.Fatal(err)
	}
	repo := strings.TrimPrefix(base.Metadata.Source.Ref, "file://")
	if err := os.MkdirAll(filepath.Join(repo, "service.yml"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "service.yml", "keep"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"checkout", "-q", "develop"}, {"add", "-A"}, {"commit", "-qm", "metadata directory"}} {
		cmd := exec.Command("git", append([]string{"-c", "user.name=test", "-c", "user.email=test@example.com"}, args...)...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	for attempt := 1; attempt <= 2; attempt++ {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/deployments/shop/environments",
			strings.NewReader(`{"name":"staging","branch":"develop"}`)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d = %d: %s", attempt, rec.Code, rec.Body.String())
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(base.Path), "shop-staging")); !os.IsNotExist(err) {
		t.Errorf("failed environment left its directory behind: %v", err)
	}
}
