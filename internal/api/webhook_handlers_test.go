package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/credentials"
	"github.com/flatrun/agent/internal/deploys"
	"github.com/flatrun/agent/internal/webhook"
	"github.com/gin-gonic/gin"
)

// newWebhookTestServer creates a git-sourced deployment whose recorded source
// is the repository the captured GitHub fixtures were delivered for.
func newWebhookTestServer(t *testing.T, runner *fakeRunner, autoDeploy bool) (*Server, *gin.Engine, string) {
	t.Helper()
	s, r := newRedeployTestServer(t, runner)
	s.credentialsManager = credentials.NewManager(t.TempDir())
	s.webhooks = webhook.DefaultRegistry()
	r.POST("/api/hooks/:provider/:deployment", s.receiveWebhook)
	r.POST("/api/deployments/:name/webhook/rotate", s.rotateDeploymentWebhook)
	r.PUT("/api/deployments/:name/source", s.updateDeploymentSource)

	repo := gitRepoWithCompose(t)
	create := httptest.NewRecorder()
	r.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/deployments",
		strings.NewReader(`{"name":"hello","source":{"type":"git","ref":"file://`+repo+`","branch":"main"}}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", create.Code, create.Body.String())
	}
	deployment, _ := s.manager.GetDeployment("hello")
	deployment.Metadata.Source.Ref = "https://github.com/Codertocat/Hello-World.git"
	if err := s.manager.SaveMetadata("hello", deployment.Metadata); err != nil {
		t.Fatal(err)
	}

	source := httptest.NewRecorder()
	r.ServeHTTP(source, httptest.NewRequest(http.MethodPut, "/api/deployments/hello/source",
		strings.NewReader(`{"branch":"master","auto_deploy":`+map[bool]string{true: "true", false: "false"}[autoDeploy]+`}`)))
	if source.Code != http.StatusOK {
		t.Fatalf("update source = %d: %s", source.Code, source.Body.String())
	}

	rotate := httptest.NewRecorder()
	r.ServeHTTP(rotate, httptest.NewRequest(http.MethodPost, "/api/deployments/hello/webhook/rotate", nil))
	if rotate.Code != http.StatusOK {
		t.Fatalf("rotate = %d: %s", rotate.Code, rotate.Body.String())
	}
	var body struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(rotate.Body.Bytes(), &body)
	if len(body.Secret) < 32 {
		t.Fatalf("secret = %q", body.Secret)
	}
	return s, r, body.Secret
}

func deliverGitHub(r *gin.Engine, event, fixture, secret string) *httptest.ResponseRecorder {
	return deliverGitHubTo(r, "hello", event, fixture, secret)
}

func deliverGitHubTo(r *gin.Engine, deployment, event, fixture, secret string) *httptest.ResponseRecorder {
	payload, _ := os.ReadFile("../webhook/testdata/" + fixture)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/github/"+deployment, strings.NewReader(string(payload)))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestWebhook_GitHubPushDeploysRevision(t *testing.T) {
	runner := &fakeRunner{optsCh: make(chan actionOptions, 1)}
	_, r, secret := newWebhookTestServer(t, runner, true)

	rec := deliverGitHub(r, "push", "github-push.json", secret)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("push = %d: %s", rec.Code, rec.Body.String())
	}
	select {
	case opts := <-runner.optsCh:
		if opts.Revision != "6113728f27ae82c7b1a177c8d03f9e96e0adf246" || opts.Trigger != string(deploys.TriggerPush) {
			t.Errorf("deploy opts = %+v", opts)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("push did not start a deploy")
	}
}

func TestWebhook_RejectsBadSignature(t *testing.T) {
	_, r, _ := newWebhookTestServer(t, &fakeRunner{}, true)
	if rec := deliverGitHub(r, "push", "github-push.json", "wrong-secret"); rec.Code != http.StatusUnauthorized {
		t.Errorf("bad signature = %d, want 401", rec.Code)
	}
}

func TestWebhook_IgnoresWhenNotApplicable(t *testing.T) {
	_, r, secret := newWebhookTestServer(t, &fakeRunner{}, false)
	if rec := deliverGitHub(r, "push", "github-push.json", secret); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "auto deploy is off") {
		t.Errorf("auto deploy off = %d %s", rec.Code, rec.Body.String())
	}
	if rec := deliverGitHub(r, "push", "github-push-tag-delete.json", secret); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ignored") {
		t.Errorf("tag delete = %d %s", rec.Code, rec.Body.String())
	}
	if rec := deliverGitHub(r, "issues", "github-push.json", secret); rec.Code != http.StatusOK {
		t.Errorf("unsupported event = %d %s", rec.Code, rec.Body.String())
	}
}

func TestWebhook_GitLabPushForOtherRepoIsIgnored(t *testing.T) {
	_, r, secret := newWebhookTestServer(t, &fakeRunner{}, true)
	payload, _ := os.ReadFile("../webhook/testdata/gitlab-push.json")
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/gitlab/hello", strings.NewReader(string(payload)))
	req.Header.Set("X-Gitlab-Event", "Push Hook")
	req.Header.Set("X-Gitlab-Token", secret)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "repository does not match") {
		t.Errorf("gitlab push for other repo = %d %s", rec.Code, rec.Body.String())
	}
}

func TestWebhook_GenericHookDeploysWithoutAutoDeploy(t *testing.T) {
	runner := &fakeRunner{optsCh: make(chan actionOptions, 1)}
	_, r, secret := newWebhookTestServer(t, runner, false)
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/generic/hello", strings.NewReader(`{"revision":"abc1234"}`))
	req.Header.Set("X-FlatRun-Token", secret)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("generic = %d: %s", rec.Code, rec.Body.String())
	}
	select {
	case opts := <-runner.optsCh:
		if opts.Revision != "abc1234" {
			t.Errorf("revision = %q", opts.Revision)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("generic hook did not start a deploy")
	}
}

func TestWebhook_UnknownDeploymentOrUnconfigured(t *testing.T) {
	s, r := newRedeployTestServer(t, &fakeRunner{})
	s.credentialsManager = credentials.NewManager(t.TempDir())
	s.webhooks = webhook.DefaultRegistry()
	r.POST("/api/hooks/:provider/:deployment", s.receiveWebhook)
	for _, path := range []string{"/api/hooks/github/missing", "/api/hooks/bitbucket/missing"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}")))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", path, rec.Code)
		}
	}
}
