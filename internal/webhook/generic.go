package webhook

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
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

func (Generic) Parse(_ http.Header, body []byte) (*Event, error) {
	var p struct {
		Branch   string `json:"branch"`
		Revision string `json:"revision"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
	}
	return &Event{Kind: EventPush, Branch: p.Branch, Revision: p.Revision}, nil
}
