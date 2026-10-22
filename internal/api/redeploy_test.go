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

	"github.com/flatrun/agent/internal/build"
	"github.com/flatrun/agent/internal/credentials"
	"github.com/flatrun/agent/internal/deploys"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/source"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
)

func newRedeployTestServer(t *testing.T, runner *fakeRunner) (*Server, *gin.Engine) {
	t.Helper()
	s := newJobTestServer(runner)
	base := t.TempDir()
	s.config = &config.Config{DeploymentsPath: base, Infrastructure: config.InfrastructureConfig{DefaultProxyNetwork: "proxy"}}
	s.manager = docker.NewManager(base)
	s.sourceRegistry = source.NewRegistry(source.GitProvider{})
	s.builders = build.DefaultRegistry()
	r := gin.New()
	r.POST("/api/deployments", s.createDeployment)
	r.POST("/api/deployments/:name/redeploy", s.redeployDeployment)
	return s, r
}

func TestRedeploy_RejectsDeploymentWithoutSource(t *testing.T) {
	s, r := newRedeployTestServer(t, &fakeRunner{})
	if err := s.manager.CreateDeployment("plain", "services:\n  app:\n    image: nginx\n", nil); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/deployments/plain/redeploy", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
}

func TestRedeploy_EnqueuesJobWithRevision(t *testing.T) {
	runner := &fakeRunner{optsCh: make(chan actionOptions, 1)}
	_, r := newRedeployTestServer(t, runner)
	repo := gitRepoWithCompose(t)

	create := httptest.NewRecorder()
	r.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/deployments",
		strings.NewReader(`{"name":"gitapp","source":{"type":"git","ref":"file://`+repo+`","branch":"main"}}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", create.Code, create.Body.String())
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/deployments/gitapp/redeploy",
		strings.NewReader(`{"revision":"abc1234"}`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("redeploy status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}

	select {
	case opts := <-runner.optsCh:
		if opts.Revision != "abc1234" {
			t.Errorf("revision = %q, want abc1234", opts.Revision)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("redeploy job never ran")
	}
}

func TestRollback_RedeploysRecordedRevision(t *testing.T) {
	runner := &fakeRunner{optsCh: make(chan actionOptions, 1)}
	s, r := newRedeployTestServer(t, runner)
	s.deploysStore = deploys.NewStore(t.TempDir())
	r.GET("/api/deployments/:name/deploys", s.listDeploymentDeploys)
	r.POST("/api/deployments/:name/deploys/:id/rollback", s.rollbackDeployment)

	repo := gitRepoWithCompose(t)
	create := httptest.NewRecorder()
	r.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/deployments",
		strings.NewReader(`{"name":"gitapp","source":{"type":"git","ref":"file://`+repo+`","branch":"main"}}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", create.Code, create.Body.String())
	}

	rec, err := s.deploysStore.Start("gitapp", deploys.Record{Trigger: deploys.TriggerPush})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.deploysStore.Finish("gitapp", rec.ID, "deadbeefcafe", "", nil); err != nil {
		t.Fatal(err)
	}

	list := httptest.NewRecorder()
	r.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/deployments/gitapp/deploys", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "deadbeefcafe") {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}

	rollback := httptest.NewRecorder()
	r.ServeHTTP(rollback, httptest.NewRequest(http.MethodPost, "/api/deployments/gitapp/deploys/"+rec.ID+"/rollback", nil))
	if rollback.Code != http.StatusAccepted {
		t.Fatalf("rollback status = %d: %s", rollback.Code, rollback.Body.String())
	}
	select {
	case opts := <-runner.optsCh:
		if opts.Revision != "deadbeefcafe" || opts.Trigger != string(deploys.TriggerRollback) {
			t.Errorf("rollback opts = %+v", opts)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rollback job never ran")
	}

	missing := httptest.NewRecorder()
	r.ServeHTTP(missing, httptest.NewRequest(http.MethodPost, "/api/deployments/gitapp/deploys/nope/rollback", nil))
	if missing.Code != http.StatusNotFound {
		t.Errorf("unknown deploy rollback = %d, want 404", missing.Code)
	}
}

func waitForJob(t *testing.T, r *gin.Engine, name, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/deployments/"+name+"/jobs/"+id, nil))
		var job map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &job)
		if status := job["status"]; status == string(JobSucceeded) || status == string(JobFailed) {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not finish: %s", id, rec.Body.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func deployRoutes(s *Server, r *gin.Engine) {
	r.GET("/api/deployments/:name/deploys", s.listDeploymentDeploys)
	r.GET("/api/deployments/:name/deploys/:id/logs", s.getDeployLogs)
	r.GET("/api/deployments/:name/jobs/:jobId", s.getDeploymentJob)
}

func listDeploys(t *testing.T, r *gin.Engine, name string) []map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/deployments/"+name+"/deploys", nil))
	var body struct {
		Deploys []map[string]any `json:"deploys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("deploys = %s", rec.Body.String())
	}
	return body.Deploys
}

func TestRedeployRecordsAFailedFetchWithItsOutput(t *testing.T) {
	s, r := newRedeployTestServer(t, &fakeRunner{})
	s.runDeploymentAction = s.defaultRunDeploymentAction
	s.credentialsManager = credentials.NewManager(t.TempDir())
	s.deploysStore = deploys.NewStore(t.TempDir())
	deployRoutes(s, r)
	repo := gitRepoWithCompose(t)
	create := httptest.NewRecorder()
	r.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/deployments",
		strings.NewReader(`{"name":"gitapp","source":{"type":"git","ref":"file://`+repo+`","branch":"main"}}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", create.Code, create.Body.String())
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/deployments/gitapp/redeploy",
		strings.NewReader(`{"revision":"0000000000000000000000000000000000000000"}`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("redeploy = %d: %s", rec.Code, rec.Body.String())
	}
	var accepted map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &accepted)
	job := waitForJob(t, r, "gitapp", accepted["job_id"].(string))
	if job["status"] != string(JobFailed) || job["phase"] != string(deploys.PhaseFailed) || job["deploy_id"] == "" {
		t.Fatalf("job = %v", job)
	}

	records := listDeploys(t, r, "gitapp")
	if len(records) != 1 {
		t.Fatalf("records = %v", records)
	}
	got := records[0]
	if got["action"] != "redeploy" || got["status"] != "failed" || got["phase"] != "failed" || got["error"] == "" || got["id"] != job["deploy_id"] {
		t.Fatalf("record = %v", got)
	}

	logs := httptest.NewRecorder()
	r.ServeHTTP(logs, httptest.NewRequest(http.MethodGet, "/api/deployments/gitapp/deploys/"+got["id"].(string)+"/logs", nil))
	if logs.Code != http.StatusOK || !strings.Contains(logs.Body.String(), "Fetching file://") {
		t.Fatalf("logs = %d %s", logs.Code, logs.Body.String())
	}
}

func TestSuccessfulDeploysTakeOverAsActive(t *testing.T) {
	runner := &fakeRunner{lines: []string{"Building image", "Started app"}}
	s, r := newRedeployTestServer(t, runner)
	s.deploysStore = deploys.NewStore(t.TempDir())
	deployRoutes(s, r)
	revisions := []string{"1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"}
	calls := 0
	s.runDeploymentAction = func(action, name string, opts actionOptions, emit func(string)) error {
		opts.progress.phase(deploys.PhaseBuilding)
		opts.progress.fetched(revisions[calls], "Add checkout page")
		calls++
		opts.progress.phase(deploys.PhaseDeploying)
		return runner.run(action, name, opts, emit)
	}
	repo := gitRepoWithCompose(t)
	create := httptest.NewRecorder()
	r.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/deployments",
		strings.NewReader(`{"name":"gitapp","source":{"type":"git","ref":"file://`+repo+`","branch":"main"}}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", create.Code, create.Body.String())
	}

	for range revisions {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/deployments/gitapp/redeploy", nil))
		var accepted map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &accepted)
		if job := waitForJob(t, r, "gitapp", accepted["job_id"].(string)); job["phase"] != string(deploys.PhaseActive) {
			t.Fatalf("job = %v", job)
		}
	}

	records := listDeploys(t, r, "gitapp")
	if len(records) != 2 {
		t.Fatalf("records = %v", records)
	}
	newest, older := records[0], records[1]
	if newest["phase"] != "active" || newest["revision"] != revisions[1] || newest["message"] != "Add checkout page" || newest["branch"] != "main" {
		t.Fatalf("newest = %v", newest)
	}
	if older["phase"] != "removed" || older["revision"] != revisions[0] {
		t.Fatalf("older = %v", older)
	}
	logs := httptest.NewRecorder()
	r.ServeHTTP(logs, httptest.NewRequest(http.MethodGet, "/api/deployments/gitapp/deploys/"+newest["id"].(string)+"/logs", nil))
	if !strings.Contains(logs.Body.String(), `Building image\nStarted app\n`) {
		t.Fatalf("logs = %s", logs.Body.String())
	}
}

func TestCreateDeploymentFromDockerfileRepo_QueuesBuild(t *testing.T) {
	runner := &fakeRunner{optsCh: make(chan actionOptions, 1)}
	s, r := newRedeployTestServer(t, runner)
	repo := t.TempDir()
	gitInit(t, repo, map[string]string{"Dockerfile": "FROM busybox\n"})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/deployments",
		strings.NewReader(`{"name":"built","auto_start":true,"container_port":3000,"source":{"type":"git","ref":"file://`+repo+`","branch":"main"}}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"build_job_id":"`) || strings.Contains(rec.Body.String(), `"build_job_id":""`) {
		t.Errorf("response should carry the build job id: %s", rec.Body.String())
	}

	select {
	case opts := <-runner.optsCh:
		if len(opts.Revision) != 40 {
			t.Errorf("build job revision = %q, want the fetched commit", opts.Revision)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("build job never ran")
	}

	deployment, err := s.manager.GetDeployment("built")
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Metadata.Source == nil || deployment.Metadata.Source.Builder != "dockerfile" {
		t.Errorf("source = %+v, want builder dockerfile", deployment.Metadata.Source)
	}
	compose, _, _ := s.manager.GetComposeFile("built")
	if !strings.Contains(compose, "image: flatrun/built:") || !strings.Contains(compose, "PORT") || !strings.Contains(compose, ".env.flatrun") {
		t.Errorf("compose should run the built image with PORT set and the managed env file loaded:\n%s", compose)
	}
	if _, err := os.Stat(filepath.Join(deployment.Path, ".env.flatrun")); err != nil {
		t.Errorf("managed env file must exist for the built image: %v", err)
	}
}

func gitInit(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-b", "main")
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-m", "init")
}
