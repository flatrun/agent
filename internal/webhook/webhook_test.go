package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func githubHeaders(event string, body []byte, secret string) http.Header {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	h := http.Header{}
	h.Set("X-GitHub-Event", event)
	h.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return h
}

func TestGitHub_VerifySignature(t *testing.T) {
	body := fixture(t, "github-push.json")
	h := githubHeaders("push", body, "s3cret")

	if err := (GitHub{}).Verify(h, body, "s3cret"); err != nil {
		t.Errorf("valid signature rejected: %v", err)
	}
	if err := (GitHub{}).Verify(h, body, "other"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("wrong secret = %v, want ErrUnauthorized", err)
	}
	if err := (GitHub{}).Verify(h, append(body, ' '), "s3cret"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("tampered body = %v, want ErrUnauthorized", err)
	}
	if err := (GitHub{}).Verify(http.Header{}, body, "s3cret"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("missing signature = %v, want ErrUnauthorized", err)
	}
}

func TestGitHub_Parse(t *testing.T) {
	cases := []struct {
		file, event string
		want        Event
	}{
		{"github-push.json", "push", Event{Kind: EventPush, Branch: "master", Revision: "6113728f27ae82c7b1a177c8d03f9e96e0adf246"}},
		{"github-push-tag-delete.json", "push", Event{Kind: EventIgnored}},
		{"github-pr-opened.json", "pull_request", Event{Kind: EventPROpened, Branch: "changes", Revision: "ec26c3e57ca3a959ca5aad62de7213c562f8c821", PR: 2}},
		{"github-pr-synchronize.json", "pull_request", Event{Kind: EventPRSynced, PR: 2}},
		{"github-pr-closed.json", "pull_request", Event{Kind: EventPRClosed, Branch: "changes", PR: 2}},
	}
	for _, tc := range cases {
		ev, err := (GitHub{}).Parse(http.Header{"X-Github-Event": {tc.event}}, fixture(t, tc.file))
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		if ev.Kind != tc.want.Kind || ev.PR != tc.want.PR ||
			(tc.want.Branch != "" && ev.Branch != tc.want.Branch) ||
			(tc.want.Revision != "" && ev.Revision != tc.want.Revision) {
			t.Errorf("%s: got %+v, want %+v", tc.file, *ev, tc.want)
		}
		if !ev.MatchesRepo("https://github.com/Codertocat/Hello-World") {
			t.Errorf("%s: should match its repository", tc.file)
		}
	}
}

func TestGitLab_VerifyAndParse(t *testing.T) {
	push := fixture(t, "gitlab-push.json")
	h := http.Header{}
	h.Set("X-Gitlab-Event", "Push Hook")
	h.Set("X-Gitlab-Token", "tok")
	if err := (GitLab{}).Verify(h, push, "tok"); err != nil {
		t.Errorf("valid token rejected: %v", err)
	}
	if err := (GitLab{}).Verify(h, push, "nope"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("wrong token = %v, want ErrUnauthorized", err)
	}

	ev, err := (GitLab{}).Parse(h, push)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != EventPush || ev.Branch != "master" || ev.Revision != "da1560886d4f094c3e6c9ef40349f7d38b5d27d7" {
		t.Errorf("push event = %+v", *ev)
	}
	if !ev.MatchesRepo("git@example.com:mike/diaspora.git") {
		t.Error("push should match the ssh address of its project")
	}

	h.Set("X-Gitlab-Event", "Merge Request Hook")
	mr, err := (GitLab{}).Parse(h, fixture(t, "gitlab-merge-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	if mr.Kind != EventPROpened || mr.PR != 16 || mr.Branch != "feature/booking-validation" {
		t.Errorf("merge request event = %+v", *mr)
	}
}

func TestGeneric(t *testing.T) {
	h := http.Header{}
	h.Set("X-FlatRun-Token", "abc")
	if err := (Generic{}).Verify(h, nil, "abc"); err != nil {
		t.Errorf("valid token rejected: %v", err)
	}
	if err := (Generic{}).Verify(http.Header{}, nil, "abc"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("missing token = %v, want ErrUnauthorized", err)
	}
	ev, err := (Generic{}).Parse(h, []byte(`{"revision":"abc123"}`))
	if err != nil || ev.Kind != EventPush || ev.Revision != "abc123" || !ev.MatchesRepo("anything") {
		t.Errorf("generic event = %+v, err = %v", ev, err)
	}
}

func TestUnsupportedEvent(t *testing.T) {
	if _, err := (GitHub{}).Parse(http.Header{"X-Github-Event": {"issues"}}, []byte(`{}`)); !errors.Is(err, ErrUnsupported) {
		t.Errorf("issues event = %v, want ErrUnsupported", err)
	}
}

func TestNormalizeRepo(t *testing.T) {
	same := []string{
		"https://github.com/Org/App.git",
		"https://user:token@github.com/org/app",
		"git@github.com:org/app.git",
		"http://github.com/org/app/",
	}
	for _, ref := range same {
		if got := NormalizeRepo(ref); got != "github.com/org/app" {
			t.Errorf("NormalizeRepo(%q) = %q", ref, got)
		}
	}
}
