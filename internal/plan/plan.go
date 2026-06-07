package plan

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const FormatVersion = 1

const (
	StatusAvailable = "available"
	StatusApplying  = "applying"
	StatusApplied   = "applied"
	StatusFailed    = "failed"
	StatusObsolete  = "obsolete"
	StatusExpired   = "expired"
)

const (
	ActionCreate = "create"
	ActionUpdate = "update"
	ActionDelete = "delete"
	ActionNoOp   = "no-op"
)

const RedactedPlaceholder = "[redacted]"

type Resource struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type Actor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type RequestEnvelope struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Params map[string]string `json:"params,omitempty"`
	Query  map[string]string `json:"query,omitempty"`
	Body   json.RawMessage   `json:"body,omitempty"`
}

type Change struct {
	Type      string   `json:"type"`
	ID        string   `json:"id"`
	Actions   []string `json:"actions"`
	Reason    string   `json:"reason"`
	Before    *string  `json:"before"`
	After     *string  `json:"after"`
	Sensitive bool     `json:"sensitive"`
}

type Snapshot struct {
	Files map[string]string `json:"files"`
}

type Summary struct {
	Create  int `json:"create"`
	Update  int `json:"update"`
	Replace int `json:"replace"`
	Delete  int `json:"delete"`
	NoOp    int `json:"no-op"`
}

type Plan struct {
	FormatVersion int             `json:"format_version"`
	ID            string          `json:"id"`
	Action        string          `json:"action"`
	Status        string          `json:"status"`
	Resource      Resource        `json:"resource"`
	CreatedAt     time.Time       `json:"created_at"`
	ExpiresAt     time.Time       `json:"expires_at"`
	CreatedBy     Actor           `json:"created_by"`
	AppliedAt     *time.Time      `json:"applied_at,omitempty"`
	AppliedBy     *Actor          `json:"applied_by,omitempty"`
	ApplyError    string          `json:"apply_error,omitempty"`
	Request       RequestEnvelope `json:"request"`
	Snapshot      Snapshot        `json:"snapshot"`
	Changes       []Change        `json:"changes"`
	Summary       Summary         `json:"summary"`
}

func New(action string, resource Resource, actor Actor, ttl time.Duration) *Plan {
	now := time.Now().UTC()
	return &Plan{
		FormatVersion: FormatVersion,
		ID:            "pln_" + uuid.New().String(),
		Action:        action,
		Status:        StatusAvailable,
		Resource:      resource,
		CreatedAt:     now,
		ExpiresAt:     now.Add(ttl),
		CreatedBy:     actor,
		Snapshot:      Snapshot{Files: map[string]string{}},
	}
}

func StrPtr(s string) *string {
	return &s
}

func (p *Plan) Expired(now time.Time) bool {
	return now.After(p.ExpiresAt)
}

// Summarize recomputes Summary from Changes. An ordered action pair
// (delete+create or create+delete) counts as one replace.
func (p *Plan) Summarize() {
	s := Summary{}
	for _, ch := range p.Changes {
		switch {
		case len(ch.Actions) == 2:
			s.Replace++
		case len(ch.Actions) == 1 && ch.Actions[0] == ActionCreate:
			s.Create++
		case len(ch.Actions) == 1 && ch.Actions[0] == ActionUpdate:
			s.Update++
		case len(ch.Actions) == 1 && ch.Actions[0] == ActionDelete:
			s.Delete++
		default:
			s.NoOp++
		}
	}
	p.Summary = s
}

// Redacted returns a copy safe for API responses: sensitive change
// contents are masked. The on-disk plan keeps full values (0600, same
// trust domain as .env.flatrun).
func (p *Plan) Redacted() *Plan {
	cp := *p
	cp.Changes = make([]Change, len(p.Changes))
	hasSensitive := false
	for i, ch := range p.Changes {
		if ch.Sensitive {
			hasSensitive = true
			if ch.Before != nil {
				ch.Before = StrPtr(RedactedPlaceholder)
			}
			if ch.After != nil {
				ch.After = StrPtr(RedactedPlaceholder)
			}
		}
		cp.Changes[i] = ch
	}
	if hasSensitive && p.Request.Body != nil {
		cp.Request.Body = json.RawMessage(`"` + RedactedPlaceholder + `"`)
	}
	return &cp
}
