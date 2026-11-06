package webhook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Envelope struct {
	SpecVersion    string            `json:"specversion"`
	ID             string            `json:"id"`
	Source         string            `json:"source"`
	Type           string            `json:"type"`
	Subject        string            `json:"subject"`
	Time           time.Time         `json:"time"`
	Data           json.RawMessage   `json:"data"`
	Attributes     map[string]string `json:"attributes"`
	Labels         []string          `json:"labels,omitempty"`
	InstallationID int64             `json:"-"`
	Event          *Event            `json:"-"`
}

func Normalize(p Provider, endpoint string, h http.Header, body []byte) (*Envelope, error) {
	ev, err := p.Parse(h, body)
	if err != nil {
		return nil, err
	}
	id := h.Get("X-GitHub-Delivery")
	if id == "" {
		id = h.Get("X-Gitlab-Event-UUID")
	}
	if id == "" {
		id = h.Get("X-Delivery-ID")
	}
	if id == "" {
		sum := sha256.Sum256(body)
		id = hex.EncodeToString(sum[:])
	}
	e := ev.Envelope
	if e == nil {
		e = &Envelope{}
	}
	e.ID = id
	e.Source = p.Name() + "/" + endpoint
	e.Data = append(json.RawMessage(nil), body...)
	e.Event = ev
	e.SpecVersion = "1.0"
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	if e.Type == "" {
		e.Type = p.Name() + "." + string(ev.Kind)
	}
	if e.Subject == "" && len(ev.RepoURLs) > 0 {
		e.Subject = NormalizeRepo(ev.RepoURLs[0])
	}
	if e.Attributes == nil {
		e.Attributes = map[string]string{}
	}
	for k, v := range map[string]string{"branch": ev.Branch, "revision": ev.Revision, "pr": strconv.Itoa(ev.PR)} {
		if _, ok := e.Attributes[k]; !ok {
			e.Attributes[k] = v
		}
	}

	e.Attributes["repository"] = e.Subject
	return e, nil
}

func (e *Envelope) Matches(source, kind string, attrs map[string]string, labels []string) bool {
	if source != "" && source != e.Source {
		return false
	}
	if kind != "" && kind != e.Type {
		return false
	}
	for k, v := range attrs {
		if e.Attributes[k] != v {
			return false
		}
	}
	for _, want := range labels {
		found := false
		for _, got := range e.Labels {
			if strings.EqualFold(want, got) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
