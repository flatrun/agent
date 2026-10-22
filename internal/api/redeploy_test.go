package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
