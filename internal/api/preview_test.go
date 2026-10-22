package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/credentials"
	"github.com/flatrun/agent/internal/deploys"
	"github.com/flatrun/agent/internal/webhook"
	"github.com/gin-gonic/gin"
)

// newPreviewTestServer has base "shop" with previews on and an existing
// preview for pull request 2 (the number in the captured GitHub fixtures),
// then points both at the fixtures' repository.
func newPreviewTestServer(t *testing.T, runner *fakeRunner, previews bool) (*Server, *gin.Engine, string) {
	t.Helper()
	s, r, _ := newEnvironmentTestServer(t, runner)
	s.credentialsManager = credentials.NewManager(t.TempDir())
	s.webhooks = webhook.DefaultRegistry()
	r.POST("/api/hooks/:provider/:deployment", s.receiveWebhook)
	r.POST("/api/deployments/:name/webhook/rotate", s.rotateDeploymentWebhook)

	if _, _, err := s.createEnvironment(context.Background(), "shop", environmentRequest{
		Name: "pr-2", Branch: "develop", PR: 2, Trigger: deploys.TriggerPR,
	}); err != nil {
		t.Fatalf("create preview: %v", err)
	}
	<-runner.optsCh

	for _, name := range []string{"shop", "shop-pr-2"} {
		d, _ := s.manager.GetDeployment(name)
		d.Metadata.Source.Ref = "https://github.com/Codertocat/Hello-World.git"
		d.Metadata.Source.Previews = previews && name == "shop"
		if err := s.manager.SaveMetadata(name, d.Metadata); err != nil {
			t.Fatal(err)
		}
	}
	rotate := httptest.NewRecorder()
	r.ServeHTTP(rotate, httptest.NewRequest(http.MethodPost, "/api/deployments/shop/webhook/rotate", nil))
	var body struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(rotate.Body.Bytes(), &body)
	return s, r, body.Secret
}

func TestPreview_SynchronizeRedeploysHeadCommit(t *testing.T) {
	runner := &fakeRunner{optsCh: make(chan actionOptions, 2)}
	s, r, secret := newPreviewTestServer(t, runner, true)

	rec := deliverGitHubTo(r, "shop", "pull_request", "github-pr-synchronize.json", secret)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "shop-pr-2") {
		t.Fatalf("synchronize = %d: %s", rec.Code, rec.Body.String())
	}
	select {
	case opts := <-runner.optsCh:
		if opts.Trigger != string(deploys.TriggerPR) || len(opts.Revision) != 40 {
			t.Errorf("preview deploy opts = %+v", opts)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("synchronize did not redeploy the preview")
	}
	preview, _ := s.manager.GetDeployment("shop-pr-2")
	if preview.Metadata.Source.Branch != "changes" {
		t.Errorf("preview branch = %q, want the PR head branch", preview.Metadata.Source.Branch)
	}
}

func TestPreview_ClosedRemovesPreview(t *testing.T) {
	s, r, secret := newPreviewTestServer(t, &fakeRunner{optsCh: make(chan actionOptions, 2)}, true)

	rec := deliverGitHubTo(r, "shop", "pull_request", "github-pr-closed.json", secret)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "removing") {
		t.Fatalf("closed = %d: %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := s.manager.GetDeployment("shop-pr-2"); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("preview deployment was not removed")
}

func TestPreview_OffIgnoresPullRequests(t *testing.T) {
	_, r, secret := newPreviewTestServer(t, &fakeRunner{optsCh: make(chan actionOptions, 2)}, false)
	rec := deliverGitHubTo(r, "shop", "pull_request", "github-pr-opened.json", secret)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "previews are off") {
		t.Errorf("opened with previews off = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPreview_OpenedStartsCreation(t *testing.T) {
	s, r, secret := newPreviewTestServer(t, &fakeRunner{optsCh: make(chan actionOptions, 2)}, true)
	// Run the creation inline and send the fixture's repository to a path that
	// does not exist, so the fetch fails locally instead of reaching GitHub.
	s.runInBackground = func(work func()) { work() }
	gitConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(gitConfig, []byte("[url \"file:///nonexistent/\"]\n\tinsteadOf = https://github.com/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	// Remove the existing preview so opening creates one.
	if _, err := s.applyDeploymentDelete("shop-pr-2", deploymentDeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	rec := deliverGitHubTo(r, "shop", "pull_request", "github-pr-opened.json", secret)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"status":"creating"`) {
		t.Errorf("opened = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := s.manager.GetDeployment("shop-pr-2"); err == nil {
		t.Error("a preview whose source cannot be fetched must not be created")
	}
}
