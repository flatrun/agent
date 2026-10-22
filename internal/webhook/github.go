package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// GitHub verifies X-Hub-Signature-256 (HMAC-SHA256 of the body with the hook
// secret) and handles push and pull_request deliveries.
type GitHub struct{}

func (GitHub) Name() string { return "github" }

func (GitHub) Verify(h http.Header, body []byte, secret string) error {
	sig := strings.TrimPrefix(h.Get("X-Hub-Signature-256"), "sha256=")
	got, err := hex.DecodeString(sig)
	if err != nil || len(got) == 0 || secret == "" {
		return ErrUnauthorized
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return ErrUnauthorized
	}
	return nil
}

type githubRepo struct {
	CloneURL string `json:"clone_url"`
	SSHURL   string `json:"ssh_url"`
	HTMLURL  string `json:"html_url"`
}

func (r githubRepo) urls() []string { return []string{r.CloneURL, r.SSHURL, r.HTMLURL} }

func (GitHub) Parse(h http.Header, body []byte) (*Event, error) {
	switch h.Get("X-GitHub-Event") {
	case "ping":
		return &Event{Kind: EventIgnored}, nil
	case "push":
		var p struct {
			Ref        string     `json:"ref"`
			After      string     `json:"after"`
			Deleted    bool       `json:"deleted"`
			Repository githubRepo `json:"repository"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		branch, ok := branchFromRef(p.Ref)
		if !ok || p.Deleted || p.After == zeroRevision {
			return &Event{Kind: EventIgnored, RepoURLs: p.Repository.urls()}, nil
		}
		return &Event{Kind: EventPush, RepoURLs: p.Repository.urls(), Branch: branch, Revision: p.After}, nil
	case "pull_request":
		var p struct {
			Action      string `json:"action"`
			Number      int    `json:"number"`
			PullRequest struct {
				Head struct {
					Ref  string     `json:"ref"`
					SHA  string     `json:"sha"`
					Repo githubRepo `json:"repo"`
				} `json:"head"`
			} `json:"pull_request"`
			Repository githubRepo `json:"repository"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		ev := &Event{
			RepoURLs: p.Repository.urls(),
			Branch:   p.PullRequest.Head.Ref,
			Revision: p.PullRequest.Head.SHA,
			PR:       p.Number,
		}
		switch p.Action {
		case "opened", "reopened":
			ev.Kind = EventPROpened
		case "synchronize":
			ev.Kind = EventPRSynced
		case "closed":
			ev.Kind = EventPRClosed
		default:
			ev.Kind = EventIgnored
		}
		return ev, nil
	default:
		return nil, ErrUnsupported
	}
}
