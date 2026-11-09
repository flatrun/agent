package webhook

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"

	"github.com/flatrun/agent/internal/events"
)

func Normalize(p Provider, endpoint string, h http.Header, body []byte) (events.Event, *Event, error) {
	var e events.Event
	var ev *Event
	var err error
	if adapter, ok := p.(interface {
		ParseEvent(http.Header, []byte) (events.Event, *Event, error)
	}); ok {
		e, ev, err = adapter.ParseEvent(h, body)
	} else {
		ev, err = p.Parse(h, body)
	}
	if err != nil {
		return e, nil, err
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
	e.ID, e.Source = id, p.Name()+"/"+endpoint
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	if e.Type == "" {
		e.Type = p.Name() + "." + string(ev.Kind)
	}
	if e.Attributes == nil {
		e.Attributes = map[string]any{}
	}
	if e.Attributes["subject"] == nil || e.Attributes["subject"] == "" {
		if len(ev.RepoURLs) > 0 {
			e.Attributes["subject"] = NormalizeRepo(ev.RepoURLs[0])
		}
	}
	for k, v := range map[string]string{"branch": ev.Branch, "revision": ev.Revision, "pr": strconv.Itoa(ev.PR)} {
		if _, ok := e.Attributes[k]; !ok {
			e.Attributes[k] = v
		}
	}
	if e.Attributes["repository"] == nil || e.Attributes["repository"] == "" {
		e.Attributes["repository"] = e.Attributes["subject"]
	}
	e.Title, e.Message, e.Severity = e.Type, e.Attribute("subject"), events.SeverityInfo
	return e, ev, nil
}
