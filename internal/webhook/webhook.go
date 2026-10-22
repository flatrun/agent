// Package webhook turns git host deliveries into deploy events. Every host is a
// Provider: it checks the delivery against the deployment's secret and reports
// what happened (a push to a branch, or a pull request opening, updating or
// closing) in one shape the agent acts on.
package webhook

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

type EventKind string

const (
	EventPush     EventKind = "push"
	EventPROpened EventKind = "pr_opened"
	EventPRSynced EventKind = "pr_synced"
	EventPRClosed EventKind = "pr_closed"
	// EventIgnored is a valid delivery the agent has nothing to do for, such
	// as a tag push, a branch deletion or a label change on a pull request.
	EventIgnored EventKind = "ignored"
)

// Event is a delivery reduced to what a deploy needs. RepoURLs lists every
// address the host gave for the repository, so it can be matched against the
// one the deployment was created from.
type Event struct {
	Kind     EventKind
	RepoURLs []string
	Branch   string
	Revision string
	PR       int
}

type Provider interface {
	Name() string
	Verify(h http.Header, body []byte, secret string) error
	Parse(h http.Header, body []byte) (*Event, error)
}

var (
	ErrUnauthorized = errors.New("webhook signature or token is invalid")
	ErrUnsupported  = errors.New("webhook event type is not supported")
)

type Registry struct {
	providers map[string]Provider
}

func NewRegistry(providers ...Provider) *Registry {
	r := &Registry{providers: make(map[string]Provider, len(providers))}
	for _, p := range providers {
		r.providers[p.Name()] = p
	}
	return r
}

func DefaultRegistry() *Registry {
	return NewRegistry(GitHub{}, GitLab{}, Generic{})
}

func (r *Registry) Get(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// MatchesRepo reports whether any of the event's repository addresses names
// the same repository as ref, ignoring scheme, credentials, case and a
// trailing .git. An event with no addresses matches, since the generic hook
// is bound to one deployment and carries none.
func (e *Event) MatchesRepo(ref string) bool {
	if len(e.RepoURLs) == 0 {
		return true
	}
	want := NormalizeRepo(ref)
	for _, u := range e.RepoURLs {
		if u != "" && NormalizeRepo(u) == want {
			return true
		}
	}
	return false
}

// NormalizeRepo reduces a repository address to host/path.
func NormalizeRepo(ref string) string {
	ref = strings.TrimSpace(ref)
	if !strings.Contains(ref, "://") {
		// scp-style git@host:owner/repo
		if at := strings.Index(ref, "@"); at >= 0 {
			ref = ref[at+1:]
		}
		ref = strings.Replace(ref, ":", "/", 1)
	} else if u, err := url.Parse(ref); err == nil {
		ref = u.Host + u.Path
	}
	ref = strings.TrimSuffix(strings.TrimSuffix(ref, "/"), ".git")
	return strings.ToLower(ref)
}

func branchFromRef(ref string) (string, bool) {
	if strings.HasPrefix(ref, "refs/heads/") {
		return strings.TrimPrefix(ref, "refs/heads/"), true
	}
	return "", false
}

const zeroRevision = "0000000000000000000000000000000000000000"
