package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
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

func parseGitHub(h http.Header, body []byte) (*Event, error) {
	switch h.Get("X-GitHub-Event") {
	case "workflow_job":
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		return &Event{Kind: EventIgnored}, nil
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

func (GitHub) Parse(h http.Header, body []byte) (*Event, error) {
	ev, err := parseGitHub(h, body)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 && h.Get("X-GitHub-Event") == "ping" {
		return ev, nil
	}
	e := &Envelope{Attributes: map[string]string{}}

	var payload struct {
		Action     string `json:"action"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		WorkflowJob struct {
			ID         int64     `json:"id"`
			HeadBranch string    `json:"head_branch"`
			HeadSHA    string    `json:"head_sha"`
			Labels     []string  `json:"labels"`
			CreatedAt  time.Time `json:"created_at"`
		} `json:"workflow_job"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	e.Type = "github." + h.Get("X-GitHub-Event")
	if payload.Action != "" {
		e.Type += "." + payload.Action
	}
	if payload.Repository.FullName != "" {
		e.Subject = payload.Repository.FullName
	}
	e.InstallationID = payload.Installation.ID
	if h.Get("X-GitHub-Event") == "workflow_job" {
		e.Labels = payload.WorkflowJob.Labels
		e.Attributes["branch"] = payload.WorkflowJob.HeadBranch
		e.Attributes["revision"] = payload.WorkflowJob.HeadSHA
		e.Attributes["job_id"] = strconv.FormatInt(payload.WorkflowJob.ID, 10)
		if !payload.WorkflowJob.CreatedAt.IsZero() {
			e.Time = payload.WorkflowJob.CreatedAt
		}
	}
	ev.Envelope = e
	return ev, nil
}
