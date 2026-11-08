package webhook

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"

	"github.com/flatrun/agent/internal/events"
)

// Generic is a deploy hook any CI system can call: X-FlatRun-Token carries the
// secret and an optional JSON body names the branch or revision to deploy.
type Generic struct{}

func (Generic) Name() string { return "generic" }

func (Generic) Verify(h http.Header, _ []byte, secret string) error {
	token := h.Get("X-FlatRun-Token")
	if secret == "" || subtle.ConstantTimeCompare([]byte(token), []byte(secret)) != 1 {
		return ErrUnauthorized
	}
	return nil
}

func (Generic) Parse(h http.Header, body []byte) (*Event, error) {
	_, parsed, err := (Generic{}).ParseEvent(h, body)
	return parsed, err
}

func (Generic) ParseEvent(_ http.Header, body []byte) (events.Event, *Event, error) {
	var p struct {
		Type       string            `json:"type"`
		Subject    string            `json:"subject"`
		Attributes map[string]string `json:"attributes"`
		Labels     []string          `json:"labels"`
		Branch     string            `json:"branch"`
		Revision   string            `json:"revision"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &p); err != nil {
			return events.Event{}, nil, err
		}
	}
	attributes := make(map[string]any, len(p.Attributes)+2)
	for key, value := range p.Attributes {
		attributes[key] = value
	}
	attributes["subject"], attributes["labels"] = p.Subject, p.Labels
	return events.Event{Type: p.Type, Attributes: attributes}, &Event{Kind: EventPush, Branch: p.Branch, Revision: p.Revision}, nil
}
