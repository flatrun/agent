package webhook

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
)

// GitLab compares X-Gitlab-Token with the hook secret and handles push and
// merge request deliveries.
type GitLab struct{}

func (GitLab) Name() string { return "gitlab" }

func (GitLab) Verify(h http.Header, _ []byte, secret string) error {
	token := h.Get("X-Gitlab-Token")
	if secret == "" || subtle.ConstantTimeCompare([]byte(token), []byte(secret)) != 1 {
		return ErrUnauthorized
	}
	return nil
}

type gitlabProject struct {
	GitHTTPURL string `json:"git_http_url"`
	GitSSHURL  string `json:"git_ssh_url"`
	WebURL     string `json:"web_url"`
}

func (p gitlabProject) urls() []string { return []string{p.GitHTTPURL, p.GitSSHURL, p.WebURL} }

func (GitLab) Parse(h http.Header, body []byte) (*Event, error) {
	switch h.Get("X-Gitlab-Event") {
	case "Push Hook":
		var p struct {
			Ref     string        `json:"ref"`
			After   string        `json:"after"`
			Project gitlabProject `json:"project"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		branch, ok := branchFromRef(p.Ref)
		if !ok || p.After == zeroRevision {
			return &Event{Kind: EventIgnored, RepoURLs: p.Project.urls()}, nil
		}
		return &Event{Kind: EventPush, RepoURLs: p.Project.urls(), Branch: branch, Revision: p.After}, nil
	case "Merge Request Hook":
		var p struct {
			Project          gitlabProject `json:"project"`
			ObjectAttributes struct {
				IID          int    `json:"iid"`
				Action       string `json:"action"`
				SourceBranch string `json:"source_branch"`
				LastCommit   struct {
					ID string `json:"id"`
				} `json:"last_commit"`
			} `json:"object_attributes"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		attrs := p.ObjectAttributes
		ev := &Event{
			RepoURLs: p.Project.urls(),
			Branch:   attrs.SourceBranch,
			Revision: attrs.LastCommit.ID,
			PR:       attrs.IID,
		}
		switch attrs.Action {
		case "open", "reopen":
			ev.Kind = EventPROpened
		case "update":
			ev.Kind = EventPRSynced
		case "close", "merge":
			ev.Kind = EventPRClosed
		default:
			ev.Kind = EventIgnored
		}
		return ev, nil
	default:
		return nil, ErrUnsupported
	}
}
