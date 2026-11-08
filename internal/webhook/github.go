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

	"github.com/flatrun/agent/internal/events"
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

type githubPayload struct {
	Action     string `json:"action"`
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Deleted    bool   `json:"deleted"`
	Number     int    `json:"number"`
	Repository struct {
		githubRepo
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest struct {
		Head struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
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

func (GitHub) Parse(h http.Header, body []byte) (*Event, error) {
	_, parsed, err := (GitHub{}).ParseEvent(h, body)
	return parsed, err
}

func (GitHub) ParseEvent(h http.Header, body []byte) (events.Event, *Event, error) {
	var p githubPayload
	kind := h.Get("X-GitHub-Event")
	switch kind {
	case "workflow_job", "ping", "push", "pull_request":
	default:
		return events.Event{}, nil, ErrUnsupported
	}
	if len(body) > 0 || kind != "ping" {
		if err := json.Unmarshal(body, &p); err != nil {
			return events.Event{}, nil, err
		}
	}
	ev := &Event{Kind: EventIgnored, RepoURLs: p.Repository.urls()}
	switch kind {
	case "workflow_job", "ping":
	case "push":
		branch, ok := branchFromRef(p.Ref)
		if ok && !p.Deleted && p.After != zeroRevision {
			ev.Kind, ev.Branch, ev.Revision = EventPush, branch, p.After
		}
	case "pull_request":
		ev.Branch, ev.Revision, ev.PR = p.PullRequest.Head.Ref, p.PullRequest.Head.SHA, p.Number
		switch p.Action {
		case "opened", "reopened":
			ev.Kind = EventPROpened
		case "synchronize":
			ev.Kind = EventPRSynced
		case "closed":
			ev.Kind = EventPRClosed
		}
	default:
		return events.Event{}, nil, ErrUnsupported
	}
	e := events.Event{Type: "github." + kind, Attributes: map[string]any{
		"subject": p.Repository.FullName, "installation_id": p.Installation.ID,
	}}
	if p.Action != "" {
		e.Type += "." + p.Action
	}
	if kind == "workflow_job" {
		e.Attributes["labels"] = p.WorkflowJob.Labels
		e.Attributes["branch"] = p.WorkflowJob.HeadBranch
		e.Attributes["revision"] = p.WorkflowJob.HeadSHA
		e.Attributes["job_id"] = strconv.FormatInt(p.WorkflowJob.ID, 10)
		e.OccurredAt = p.WorkflowJob.CreatedAt
	}
	return e, ev, nil
}
