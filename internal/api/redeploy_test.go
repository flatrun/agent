package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/deploys"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/source"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
)

func newRedeployTestServer(t *testing.T, runner *fakeRunner) (*Server, *gin.Engine) {
	t.Helper()
	s := newJobTestServer(runner)
	s.config = &config.Config{Infrastructure: config.InfrastructureConfig{DefaultProxyNetwork: "proxy"}}
	s.manager = docker.NewManager(t.TempDir())
	s.sourceRegistry = source.NewRegistry(source.GitProvider{})
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
	if err := s.deploysStore.Finish("gitapp", rec.ID, "deadbeefcafe", nil); err != nil {
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

func TestRedeployFromSource_RecordsFailedDeploy(t *testing.T) {
	s, r := newRedeployTestServer(t, &fakeRunner{})
	s.deploysStore = deploys.NewStore(t.TempDir())
	repo := gitRepoWithCompose(t)
	create := httptest.NewRecorder()
	r.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/deployments",
		strings.NewReader(`{"name":"gitapp","source":{"type":"git","ref":"file://`+repo+`","branch":"main"}}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", create.Code, create.Body.String())
	}

	err := s.redeployFromSource(context.Background(), "gitapp",
		actionOptions{Revision: "0000000000000000000000000000000000000000", Trigger: string(deploys.TriggerPush)},
		nil, func(string) {})
	if err == nil {
		t.Fatal("expected fetching a missing revision to fail")
	}

	records, _ := s.deploysStore.List("gitapp")
	if len(records) != 1 {
		t.Fatalf("records = %+v, want one", records)
	}
	if records[0].Status != deploys.StatusFailed || records[0].Trigger != deploys.TriggerPush || records[0].Error == "" {
		t.Errorf("record = %+v", records[0])
	}
}
