package observ

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// streamRef is one open stream. Rules watching the same deployment share one, so ten rules
// on one app cost one reader.
type streamRef struct {
	Deployment string
	Service    string
	Source     string
}

func (s streamRef) key() string { return s.Deployment + "\x00" + s.Service + "\x00" + s.Source }

type agentLine struct {
	Type   string `json:"type"`
	Line   string `json:"line"`
	Record struct {
		Timestamp string `json:"timestamp"`
		Service   string `json:"service"`
		Level     string `json:"level"`
		Message   string `json:"message"`
	} `json:"record"`
}

// LogWatcher keeps a reader open per stream the enabled rules need and feeds the engine.
type LogWatcher struct {
	engine  *LogEngine
	base    string
	token   string
	client  *http.Client
	rules   func() []LogRule
	respond func(incident Incident, responders []string)

	raised chan Incident

	mu      sync.Mutex
	running map[string]context.CancelFunc
}

// Deep enough that the funnel's cooldown makes filling it mean something is already very wrong.
const incidentQueueDepth = 256

func NewLogWatcher(engine *LogEngine, base, token string) *LogWatcher {
	return &LogWatcher{
		engine: engine,
		base:   strings.TrimRight(base, "/"),
		token:  token,
		// No timeout: a follow never ends on purpose. Cancellation is by context.
		client:  &http.Client{},
		raised:  make(chan Incident, incidentQueueDepth),
		running: map[string]context.CancelFunc{},
	}
}

func (w *LogWatcher) OnIncident(fn func(incident Incident, responders []string)) {
	w.respond = fn
}

func (w *LogWatcher) SetRules(rules func() []LogRule) { w.rules = rules }

// Run reconciles open streams with what the rules ask for. Rules change while it runs, so
// this is a loop rather than one-time setup.
func (w *LogWatcher) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go w.explainAndRespond(ctx)
	w.reconcile(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			w.stopAll()
			return
		case <-ticker.C:
			w.reconcile(ctx)
		}
	}
}

func (w *LogWatcher) desired() map[string]streamRef {
	want := map[string]streamRef{}
	if w.rules == nil {
		return want
	}
	for _, r := range w.rules() {
		if !r.Enabled || r.Deployment == "" {
			continue
		}
		r = r.WithDefaults()
		ref := streamRef{Deployment: r.Deployment, Service: r.Service, Source: r.Source}
		want[ref.key()] = ref
	}
	return want
}

func (w *LogWatcher) reconcile(ctx context.Context) {
	want := w.desired()

	w.mu.Lock()
	defer w.mu.Unlock()

	for key, cancel := range w.running {
		if _, ok := want[key]; !ok {
			cancel()
			delete(w.running, key)
		}
	}
	for key, ref := range want {
		if _, ok := w.running[key]; ok {
			continue
		}
		streamCtx, cancel := context.WithCancel(ctx)
		w.running[key] = cancel
		go w.follow(streamCtx, ref)
	}
}

func (w *LogWatcher) stopAll() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for key, cancel := range w.running {
		cancel()
		delete(w.running, key)
	}
}

// follow keeps one stream open, reconnecting with a backoff. A stopped deployment is normal
// here rather than an error, so only the first failure of a run is logged.
func (w *LogWatcher) follow(ctx context.Context, ref streamRef) {
	backoff := 2 * time.Second
	const maxBackoff = time.Minute
	complained := false

	for ctx.Err() == nil {
		err := w.readOnce(ctx, ref)
		if ctx.Err() != nil {
			return
		}
		if err != nil && !complained {
			log.Printf("observability: log watch on %s paused: %v", ref.Deployment, err)
			complained = true
		}
		if err == nil {
			backoff = 2 * time.Second
			complained = false
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

func (w *LogWatcher) readOnce(ctx context.Context, ref streamRef) error {
	params := url.Values{}
	params.Set("deployment", ref.Deployment)
	if ref.Service != "" {
		params.Set("service", ref.Service)
	}
	if ref.Source != "" {
		params.Set("source", ref.Source)
	}
	// No backlog: replaying history would re-raise incidents already dealt with.
	params.Set("tail", "0")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.base+"/internal/logs/stream?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Plugin-Token", w.token)

	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("log stream returned %s", resp.Status)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var incoming agentLine
		if err := json.Unmarshal(raw, &incoming); err != nil {
			continue
		}
		if incoming.Type != "log" {
			continue
		}

		service := incoming.Record.Service
		if service == "" {
			service = ref.Service
		}
		message := incoming.Record.Message
		if message == "" {
			message = incoming.Line
		}

		w.handle(LogLine{
			Deployment: ref.Deployment,
			Service:    service,
			Source:     ref.Source,
			Level:      incoming.Record.Level,
			Message:    message,
			Raw:        incoming.Line,
			At:         lineTime(incoming.Record.Timestamp),
		})
	}
	return scanner.Err()
}

// lineTime prefers when the line was written over when it was read: a reconnect would otherwise
// squeeze a spread-out burst into an instant. A stamp far from now is a wrong clock, not
// information, so arrival time stands in.
func lineTime(stamp string) time.Time {
	now := time.Now().UTC()
	if stamp == "" {
		return now
	}
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return now
	}
	if drift := now.Sub(at); drift > maxLineClockDrift || drift < -maxLineClockDrift {
		return now
	}
	return at.UTC()
}

const maxLineClockDrift = 10 * time.Minute

func (w *LogWatcher) handle(line LogLine) {
	for _, incident := range w.engine.Offer(line) {
		select {
		case w.raised <- incident:
		default:
			// The incident is recorded either way, so it reaches the page unexplained rather
			// than holding up the reader.
			log.Printf("observ: incident queue full, %s goes unexplained", incident.Key())
		}
	}
}

// explainAndRespond runs the slow half of an incident one at a time, so neither the model call
// nor the responders can hold up reading logs, and a storm of incidents cannot become a storm of
// model calls.
func (w *LogWatcher) explainAndRespond(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case incident := <-w.raised:
			if verdict := w.engine.Explain(incident); verdict != nil {
				incident.Triage = verdict
			}
			if w.respond != nil {
				w.respond(incident, w.respondersFor(incident.RuleID))
			}
		}
	}
}

func (w *LogWatcher) respondersFor(ruleID string) []string {
	if w.rules == nil {
		return []string{ResponderNotify}
	}
	for _, r := range w.rules() {
		if r.ID == ruleID {
			return r.WithDefaults().Responders
		}
	}
	return []string{ResponderNotify}
}
