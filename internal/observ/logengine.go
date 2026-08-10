package observ

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// LogLine is one parsed line, in the shape the agent's log stream already produces.
type LogLine struct {
	Deployment string
	Service    string
	Source     string
	Level      string
	Message    string
	Raw        string
	At         time.Time
}

// Triage is what the assistant concluded. Every field is optional: an incident that was
// never triaged is still a complete incident.
type Triage struct {
	Summary    string    `json:"summary,omitempty"`
	Cause      string    `json:"cause,omitempty"`
	NextStep   string    `json:"next_step,omitempty"`
	Severity   string    `json:"severity,omitempty"`
	Confidence string    `json:"confidence,omitempty"`
	Skipped    string    `json:"skipped,omitempty"`
	At         time.Time `json:"at,omitempty"`
}

// Incident is one distinct fault, seen enough times to be worth raising.
type Incident struct {
	ID          string            `json:"id"`
	RuleID      string            `json:"rule_id"`
	RuleName    string            `json:"rule_name"`
	Deployment  string            `json:"deployment"`
	Service     string            `json:"service,omitempty"`
	Source      string            `json:"source,omitempty"`
	Level       string            `json:"level"`
	Fingerprint string            `json:"fingerprint"`
	Sample      string            `json:"sample"`
	Context     []string          `json:"context,omitempty"`
	Count       int               `json:"count"`
	FirstSeen   time.Time         `json:"first_seen"`
	LastSeen    time.Time         `json:"last_seen"`
	Targets     []string          `json:"targets,omitempty"`
	Triage      *Triage           `json:"triage,omitempty"`
	Responses   []ResponderResult `json:"responses,omitempty"`
}

// Key identifies the fault rather than this sighting of it, so the same crash next week has
// the same key. A responder keys its own work on it.
func (i Incident) Key() string {
	return i.Deployment + "/" + i.RuleID + "/" + i.Fingerprint
}

func (i Incident) Title() string {
	where := i.Deployment
	if i.Service != "" {
		where += "/" + i.Service
	}
	return fmt.Sprintf("%s: %s", i.RuleName, where)
}

// Message leads with the triage when there is one, and the line itself when there is not.
func (i Incident) Message() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d occurrences in %s", i.Count, i.Deployment)
	if i.Service != "" {
		fmt.Fprintf(&b, " (%s)", i.Service)
	}
	b.WriteString(".\n\n")
	if i.Triage != nil && i.Triage.Summary != "" {
		b.WriteString(i.Triage.Summary)
		if i.Triage.Cause != "" {
			fmt.Fprintf(&b, "\n\nLikely cause: %s", i.Triage.Cause)
		}
		if i.Triage.NextStep != "" {
			fmt.Fprintf(&b, "\nSuggested next step: %s", i.Triage.NextStep)
		}
		b.WriteString("\n\n")
	}
	b.WriteString(i.Sample)
	return b.String()
}

type window struct {
	times     []time.Time
	lastFired time.Time
	incident  *Incident
}

// LogEngine runs the funnel: level, pattern, burst, fingerprint cooldown, then whatever the
// rule asked for. Everything before the last step is local, so a container writing a line a
// millisecond costs a regex per line and nothing else.
type LogEngine struct {
	mu       sync.Mutex
	rules    []LogRule
	patterns map[string]*regexp.Regexp
	windows  map[string]*window
	recent   []Incident
	now      func() time.Time

	// Nil means incidents are raised untriaged rather than not raised.
	triage       func(ctx context.Context, incident Incident) (*Triage, error)
	contextLines int
	recentLines  map[string][]string
}

const maxRecentIncidents = 200

func NewLogEngine() *LogEngine {
	return &LogEngine{
		patterns:     map[string]*regexp.Regexp{},
		windows:      map[string]*window{},
		recentLines:  map[string][]string{},
		now:          func() time.Time { return time.Now().UTC() },
		contextLines: 12,
	}
}

// OnTriage registers what explains an incident, called only for rules that asked for it.
func (e *LogEngine) OnTriage(fn func(ctx context.Context, incident Incident) (*Triage, error)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.triage = fn
}

func (e *LogEngine) SetContextLines(n int) {
	if n <= 0 {
		return
	}
	if n > maxLogContextLines {
		n = maxLogContextLines
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.contextLines = n
}

// SetRules replaces the rule set, forgetting the state of rules that no longer exist.
func (e *LogEngine) SetRules(rules []LogRule) {
	e.mu.Lock()
	defer e.mu.Unlock()

	live := map[string]bool{}
	prepared := make([]LogRule, 0, len(rules))
	compiled := map[string]*regexp.Regexp{}
	for _, r := range rules {
		r = r.WithDefaults()
		if r.Pattern != "" {
			re, err := regexp.Compile(r.Pattern)
			if err != nil {
				// Dropping one uncompilable rule beats losing the whole set.
				continue
			}
			compiled[r.ID] = re
		}
		prepared = append(prepared, r)
		live[r.ID] = true
	}
	e.rules = prepared
	e.patterns = compiled

	for key := range e.windows {
		if id, _, ok := strings.Cut(key, "\x00"); ok && !live[id] {
			delete(e.windows, key)
		}
	}
}

func (e *LogEngine) Rules() []LogRule {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append(make([]LogRule, 0, len(e.rules)), e.rules...)
}

func (e *LogEngine) Incidents() []Incident {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append(make([]Incident, 0, len(e.recent)), e.recent...)
}

// Offer runs one line through the funnel, returning what it raised. The caller acts; the
// engine only decides.
func (e *LogEngine) Offer(line LogLine) []Incident {
	e.mu.Lock()

	if line.At.IsZero() {
		line.At = e.now()
	}
	e.rememberLine(line)

	var raised []Incident
	for _, rule := range e.rules {
		if !rule.Enabled {
			continue
		}
		if !e.matches(rule, line) {
			continue
		}
		if incident := e.record(rule, line); incident != nil {
			raised = append(raised, *incident)
		}
	}
	triage := e.triage
	e.mu.Unlock()

	// Outside the lock: this reaches a model, and holding the engine would stall every
	// other line on the host.
	for i := range raised {
		if raised[i].Triage != nil || triage == nil {
			continue
		}
		if !e.ruleWantsTriage(raised[i].RuleID) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		verdict, err := triage(ctx, raised[i])
		cancel()
		if err != nil {
			verdict = &Triage{Skipped: err.Error(), At: e.now()}
		}
		if verdict != nil {
			raised[i].Triage = verdict
			e.attachTriage(raised[i].ID, verdict)
		}
	}
	return raised
}

func (e *LogEngine) ruleWantsTriage(ruleID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range e.rules {
		if r.ID == ruleID {
			return r.Triage
		}
	}
	return false
}

func (e *LogEngine) attachTriage(incidentID string, verdict *Triage) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.recent {
		if e.recent[i].ID == incidentID {
			e.recent[i].Triage = verdict
			return
		}
	}
}

// AttachResponses records what the responders did.
func (e *LogEngine) AttachResponses(incidentID string, results []ResponderResult) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.recent {
		if e.recent[i].ID == incidentID {
			e.recent[i].Responses = append(e.recent[i].Responses, results...)
			return
		}
	}
}

func (e *LogEngine) matches(rule LogRule, line LogLine) bool {
	if rule.Deployment != line.Deployment {
		return false
	}
	if rule.Service != "" && rule.Service != line.Service {
		return false
	}
	if rule.Source != "" && line.Source != "" && rule.Source != line.Source {
		return false
	}
	if !rule.matchesLevel(line.Level) {
		return false
	}
	if re, ok := e.patterns[rule.ID]; ok && !re.MatchString(line.Message) {
		return false
	}
	return true
}

// record applies the burst threshold and the cooldown, returning an incident only when this
// line crosses from noise into news.
func (e *LogEngine) record(rule LogRule, line LogLine) *Incident {
	fp := fingerprint(line.Message)
	key := rule.ID + "\x00" + line.Deployment + "\x00" + line.Service + "\x00" + fp

	w := e.windows[key]
	if w == nil {
		w = &window{}
		e.windows[key] = w
	}

	cutoff := line.At.Add(-time.Duration(rule.WindowSeconds) * time.Second)
	kept := w.times[:0]
	for _, t := range w.times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	w.times = append(kept, line.At)

	// Already reported and still inside the quiet period: count it and say nothing.
	if !w.lastFired.IsZero() && line.At.Sub(w.lastFired) < time.Duration(rule.CooldownSeconds)*time.Second {
		if w.incident != nil {
			w.incident.Count++
			w.incident.LastSeen = line.At
			e.bumpRecent(w.incident.ID, line.At)
		}
		return nil
	}

	if len(w.times) < rule.MinCount {
		return nil
	}

	incident := Incident{
		ID:          fmt.Sprintf("%s-%d", fp, line.At.UnixNano()),
		RuleID:      rule.ID,
		RuleName:    rule.Name,
		Deployment:  line.Deployment,
		Service:     line.Service,
		Source:      line.Source,
		Level:       line.Level,
		Fingerprint: fp,
		Sample:      line.Raw,
		Context:     e.contextFor(line),
		Count:       len(w.times),
		FirstSeen:   w.times[0],
		LastSeen:    line.At,
		Targets:     rule.Targets,
	}
	w.lastFired = line.At
	w.incident = &incident
	w.times = w.times[:0]

	e.recent = append(e.recent, incident)
	if len(e.recent) > maxRecentIncidents {
		e.recent = e.recent[len(e.recent)-maxRecentIncidents:]
	}
	return &incident
}

func (e *LogEngine) bumpRecent(incidentID string, at time.Time) {
	for i := range e.recent {
		if e.recent[i].ID == incidentID {
			e.recent[i].Count++
			e.recent[i].LastSeen = at
			return
		}
	}
}

func (e *LogEngine) streamKey(line LogLine) string {
	return line.Deployment + "\x00" + line.Service + "\x00" + line.Source
}

func (e *LogEngine) rememberLine(line LogLine) {
	key := e.streamKey(line)
	tail := append(e.recentLines[key], line.Raw)
	if len(tail) > maxLogContextLines {
		tail = tail[len(tail)-maxLogContextLines:]
	}
	e.recentLines[key] = tail
}

func (e *LogEngine) contextFor(line LogLine) []string {
	tail := e.recentLines[e.streamKey(line)]
	if len(tail) <= e.contextLines {
		return append([]string(nil), tail...)
	}
	return append([]string(nil), tail[len(tail)-e.contextLines:]...)
}
