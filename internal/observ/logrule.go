package observ

import (
	"fmt"
	"regexp"
	"strings"
)

// Log levels a rule can gate on, ordered by severity.
const (
	LogLevelDebug = "debug"
	LogLevelInfo  = "info"
	LogLevelWarn  = "warn"
	LogLevelError = "error"
	LogLevelFatal = "fatal"
)

var logLevelRank = map[string]int{
	LogLevelDebug: 1,
	LogLevelInfo:  2,
	LogLevelWarn:  3,
	LogLevelError: 4,
	LogLevelFatal: 5,
}

// Defaults chosen so a rule written without thinking still cannot run up a bill.
const (
	defaultLogMinCount   = 3
	defaultLogWindowSecs = 300
	defaultLogCooldown   = 3600
	// Bounds what an incident keeps, and so the most a triage can ever be asked to read.
	maxLogContextLines = 40
)

// LogRule turns lines a deployment writes into an incident. Every field except Triage exists
// to make the expensive part rare, so a model is only asked about what survives all of them.
type LogRule struct {
	ID         string `json:"id" yaml:"id"`
	Name       string `json:"name" yaml:"name"`
	Enabled    bool   `json:"enabled" yaml:"enabled"`
	Deployment string `json:"deployment" yaml:"deployment"`
	Service    string `json:"service,omitempty" yaml:"service,omitempty"`
	Source     string `json:"source,omitempty" yaml:"source,omitempty"`
	// Defaults to error: a rule watching info is a rule watching everything.
	MinLevel string `json:"min_level,omitempty" yaml:"min_level,omitempty"`
	// Matched against the parsed message, not the raw line, so it cannot hit a timestamp
	// or a service name.
	Pattern       string `json:"pattern,omitempty" yaml:"pattern,omitempty"`
	MinCount      int    `json:"min_count,omitempty" yaml:"min_count,omitempty"`
	WindowSeconds int    `json:"window_seconds,omitempty" yaml:"window_seconds,omitempty"`
	// Repeats inside the cooldown are counted onto the open incident rather than raising
	// another.
	CooldownSeconds int `json:"cooldown_seconds,omitempty" yaml:"cooldown_seconds,omitempty"`
	// The only field that costs money to run, so it is off unless asked for.
	Triage     bool     `json:"triage,omitempty" yaml:"triage,omitempty"`
	Responders []string `json:"responders,omitempty" yaml:"responders,omitempty"`
	Targets    []string `json:"targets,omitempty" yaml:"targets,omitempty"`
}

// WithDefaults fills the fields a rule may leave empty.
func (r LogRule) WithDefaults() LogRule {
	if r.MinLevel == "" {
		r.MinLevel = LogLevelError
	}
	if r.Source == "" {
		r.Source = "stdout"
	}
	if r.MinCount <= 0 {
		r.MinCount = defaultLogMinCount
	}
	if r.WindowSeconds <= 0 {
		r.WindowSeconds = defaultLogWindowSecs
	}
	if r.CooldownSeconds <= 0 {
		r.CooldownSeconds = defaultLogCooldown
	}
	if len(r.Responders) == 0 {
		r.Responders = []string{ResponderNotify}
	}
	return r
}

// Validate reports why a rule cannot be used, if it cannot.
func (r LogRule) Validate() error {
	r = r.WithDefaults()

	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("a rule needs a name")
	}
	if strings.TrimSpace(r.Deployment) == "" {
		return fmt.Errorf("a log rule needs a deployment to watch")
	}
	if _, ok := logLevelRank[r.MinLevel]; !ok {
		return fmt.Errorf("unknown level %q", r.MinLevel)
	}
	// This broad a rule matches most of what a chatty application writes.
	if logLevelRank[r.MinLevel] < logLevelRank[LogLevelError] && strings.TrimSpace(r.Pattern) == "" {
		return fmt.Errorf("a rule below error level needs a pattern, or it matches nearly every line")
	}
	if r.Pattern != "" {
		if _, err := regexp.Compile(r.Pattern); err != nil {
			return fmt.Errorf("pattern is not a valid regular expression: %w", err)
		}
	}
	if r.MinCount > 1000 {
		return fmt.Errorf("min_count above 1000 would never fire in a useful window")
	}
	for _, name := range r.Responders {
		if !knownResponder(name) {
			return fmt.Errorf("unknown responder %q", name)
		}
	}
	return nil
}

// matchesLevel reports whether a parsed level is severe enough. An unparseable level does not
// match, so it cannot slip past a rule that asked for errors.
func (r LogRule) matchesLevel(level string) bool {
	rank, ok := logLevelRank[strings.ToLower(strings.TrimSpace(level))]
	if !ok {
		return false
	}
	return rank >= logLevelRank[r.WithDefaults().MinLevel]
}
