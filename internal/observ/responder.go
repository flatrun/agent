package observ

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

const (
	ResponderNotify = "notify"
)

// Responder acts on an incident: notifying, filing an issue, handing it to an agent that
// opens a pull request. A new one is a registration rather than a change to the engine.
//
// One that reaches an external system should key its work on Incident.Key(), so a retry
// cannot produce a second issue for one fault.
type Responder interface {
	Name() string
	// Respond describes what it did in a sentence; both that and any error are recorded.
	Respond(ctx context.Context, incident Incident) (string, error)
}

type ResponderFunc struct {
	ResponderName string
	Fn            func(ctx context.Context, incident Incident) (string, error)
}

func (r ResponderFunc) Name() string { return r.ResponderName }

func (r ResponderFunc) Respond(ctx context.Context, incident Incident) (string, error) {
	return r.Fn(ctx, incident)
}

type ResponderResult struct {
	Responder string    `json:"responder"`
	Detail    string    `json:"detail,omitempty"`
	Error     string    `json:"error,omitempty"`
	At        time.Time `json:"at"`
}

// Package-level because rule validation runs wherever a rule is saved.
var registry = struct {
	mu sync.RWMutex
	m  map[string]Responder
}{m: map[string]Responder{}}

// RegisterResponder makes a responder available to rules, replacing one of the same name.
func RegisterResponder(r Responder) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.m[r.Name()] = r
}

func KnownResponders() []string {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	names := make([]string, 0, len(registry.m))
	for name := range registry.m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func knownResponder(name string) bool {
	// Always valid: a rule saved before its responder registers must not be rejected.
	if name == ResponderNotify {
		return true
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	_, ok := registry.m[name]
	return ok
}

func lookupResponder(name string) (Responder, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	r, ok := registry.m[name]
	return r, ok
}

// runResponders runs a rule's responders in order. One failing never stops the others: an
// incident that could not be filed must still reach a human.
func runResponders(ctx context.Context, names []string, incident Incident) []ResponderResult {
	results := make([]ResponderResult, 0, len(names))
	for _, name := range names {
		r, ok := lookupResponder(name)
		if !ok {
			results = append(results, ResponderResult{
				Responder: name,
				Error:     fmt.Sprintf("responder %q is not available in this build", name),
				At:        time.Now().UTC(),
			})
			continue
		}
		detail, err := r.Respond(ctx, incident)
		res := ResponderResult{Responder: name, Detail: detail, At: time.Now().UTC()}
		if err != nil {
			res.Error = err.Error()
		}
		results = append(results, res)
	}
	return results
}

// NewNotifyResponder delivers the incident to the operator's configured targets.
func NewNotifyResponder(send func(title, message string, targets []string)) Responder {
	return ResponderFunc{
		ResponderName: ResponderNotify,
		Fn: func(_ context.Context, incident Incident) (string, error) {
			if send == nil {
				return "", fmt.Errorf("no notification transport")
			}
			send(incident.Title(), incident.Message(), incident.Targets)
			return "notified", nil
		},
	}
}
