package observ

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The other half of the wire contract pinned in the agent's TestInternalLogEnvelopeShape: the
// watcher decodes what that envelope marshals to, including the level the agent parsed, so a
// rule watching errors matches without the app parsing a line a second time.
func TestLogWatcherRaisesFromTheAgentsEnvelope(t *testing.T) {
	lines := []string{
		`{"type":"log","line":"web-1  | 2026-08-06T12:00:31.123456Z ERROR connection refused talking to redis","record":{"timestamp":"2026-08-06T12:00:31.123456Z","service":"web-1","level":"error","message":"ERROR connection refused talking to redis","raw":"web-1  | 2026-08-06T12:00:31.123456Z ERROR connection refused talking to redis"}}`,
		`{"type":"log","line":"web-1  | 2026-08-06T12:00:32.000000Z INFO serving request","record":{"timestamp":"2026-08-06T12:00:32.000000Z","service":"web-1","level":"info","message":"INFO serving request","raw":"web-1  | 2026-08-06T12:00:32.000000Z INFO serving request"}}`,
		`{"type":"log","line":"web-1  | 2026-08-06T12:00:33.000000Z ERROR connection refused talking to redis","record":{"timestamp":"2026-08-06T12:00:33.000000Z","service":"web-1","level":"error","message":"ERROR connection refused talking to redis","raw":"web-1  | 2026-08-06T12:00:33.000000Z ERROR connection refused talking to redis"}}`,
	}

	var gotToken string
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Plugin-Token")
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, line := range lines {
			fmt.Fprintln(w, line)
			if flusher != nil {
				flusher.Flush()
			}
		}
		// Hold the response open the way a real follow does, until the watcher leaves.
		<-r.Context().Done()
	}))
	defer server.Close()

	engine := NewLogEngine()
	rule := LogRule{
		ID:         "rule-1",
		Name:       "App errors",
		Enabled:    true,
		Deployment: "shop",
		MinCount:   2,
	}
	engine.SetRules([]LogRule{rule.WithDefaults()})

	watcher := NewLogWatcher(engine, server.URL, "plugin-secret")
	watcher.SetRules(engine.Rules)

	var (
		mu        sync.Mutex
		incidents []Incident
	)
	got := make(chan struct{})
	var once sync.Once
	watcher.OnIncident(func(incident Incident, responders []string) {
		mu.Lock()
		incidents = append(incidents, incident)
		mu.Unlock()
		once.Do(func() { close(got) })
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go watcher.Run(ctx, time.Hour)

	select {
	case <-got:
	case <-ctx.Done():
		t.Fatal("the watcher never raised an incident from the agent's lines")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(incidents) != 1 {
		t.Fatalf("two matching errors and one info line should raise one incident, got %d", len(incidents))
	}
	incident := incidents[0]
	if incident.Deployment != "shop" {
		t.Errorf("deployment = %q", incident.Deployment)
	}
	if incident.Service != "web-1" {
		t.Errorf("service should come from the record the agent parsed, got %q", incident.Service)
	}
	if incident.Level != "error" {
		t.Errorf("level should come from the record the agent parsed, got %q", incident.Level)
	}
	if incident.Count != 2 {
		t.Errorf("count = %d, want the two matching lines", incident.Count)
	}
	if gotToken != "plugin-secret" {
		t.Errorf("the watcher must authenticate with the plugin token, got %q", gotToken)
	}
	// Replaying history on reconnect would re-raise incidents that were already handled.
	if !strings.Contains(gotQuery, "tail=0") {
		t.Errorf("the watcher should ask for no backlog, got query %q", gotQuery)
	}
}

// The model call is the slowest thing in the pipeline, and a reader that waits on it stops
// reading the deployment's logs for as long as it takes.
func TestLogWatcherKeepsReadingWhileTriageIsSlow(t *testing.T) {
	lines := []string{
		`{"type":"log","line":"web-1  | ERROR connection refused talking to redis","record":{"service":"web-1","level":"error","message":"connection refused talking to redis"}}`,
		`{"type":"log","line":"web-1  | ERROR disk is full","record":{"service":"web-1","level":"error","message":"disk is full"}}`,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, line := range lines {
			fmt.Fprintln(w, line)
			if flusher != nil {
				flusher.Flush()
			}
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	triaging := make(chan struct{})
	release := make(chan struct{})
	engine := NewLogEngine()
	engine.OnTriage(func(_ context.Context, _ Incident) (*Triage, error) {
		close(triaging)
		<-release
		return &Triage{Summary: "redis is down"}, nil
	})
	engine.SetRules([]LogRule{
		LogRule{ID: "redis", Name: "Redis", Enabled: true, Deployment: "shop", Pattern: "redis", MinCount: 1, Triage: true}.WithDefaults(),
		LogRule{ID: "disk", Name: "Disk", Enabled: true, Deployment: "shop", Pattern: "disk", MinCount: 1}.WithDefaults(),
	})

	watcher := NewLogWatcher(engine, server.URL, "plugin-secret")
	watcher.SetRules(engine.Rules)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go watcher.Run(ctx, time.Hour)
	defer close(release)

	select {
	case <-triaging:
	case <-ctx.Done():
		t.Fatal("triage never ran")
	}

	// Triage is still blocked here, so the second line only lands if reading kept going.
	deadline := time.Now().Add(3 * time.Second)
	for {
		for _, incident := range engine.Incidents() {
			if incident.RuleID == "disk" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the reader stopped taking lines while the model was answering")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A stream that is not there is a normal condition (a stopped deployment, a restarting
// agent), so the watcher keeps trying rather than giving up on the rule.
func TestLogWatcherKeepsTryingWhenTheStreamIsUnavailable(t *testing.T) {
	var attempts int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		http.Error(w, "deployment not found", http.StatusNotFound)
	}))
	defer server.Close()

	engine := NewLogEngine()
	missing := LogRule{ID: "r", Name: "n", Enabled: true, Deployment: "gone"}
	engine.SetRules([]LogRule{missing.WithDefaults()})

	watcher := NewLogWatcher(engine, server.URL, "plugin-secret")
	watcher.SetRules(engine.Rules)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	go watcher.Run(ctx, time.Hour)

	deadline := time.After(6 * time.Second)
	for {
		mu.Lock()
		n := attempts
		mu.Unlock()
		if n >= 2 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("expected the watcher to retry, saw %d attempts", n)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// A rule that is deleted should take its reader with it.
func TestLogWatcherClosesStreamsItNoLongerNeeds(t *testing.T) {
	open := make(chan struct{}, 4)
	closed := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		open <- struct{}{}
		<-r.Context().Done()
		closed <- struct{}{}
	}))
	defer server.Close()

	engine := NewLogEngine()
	watched := LogRule{ID: "r", Name: "n", Enabled: true, Deployment: "shop"}
	engine.SetRules([]LogRule{watched.WithDefaults()})

	watcher := NewLogWatcher(engine, server.URL, "plugin-secret")
	watcher.SetRules(engine.Rules)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go watcher.Run(ctx, 200*time.Millisecond)

	select {
	case <-open:
	case <-ctx.Done():
		t.Fatal("the watcher never opened a stream")
	}

	engine.SetRules(nil)

	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("the watcher kept reading a stream no rule asked for")
	}
}
