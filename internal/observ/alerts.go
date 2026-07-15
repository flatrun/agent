package observ

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Comparison is how a rule's threshold is read.
const (
	ComparisonAbove = "above"
	ComparisonBelow = "below"
)

// AlertState is where a rule stands.
const (
	AlertOK      = "ok"
	AlertPending = "pending"
	AlertFiring  = "firing"
)

// AlertRule fires when a metric stays past a threshold for long enough.
//
// The duration is what separates an alert from a twitch: a container is briefly at 100% CPU
// every time it starts, and a rule without one would page an operator for it.
type AlertRule struct {
	ID         string  `json:"id" yaml:"id"`
	Name       string  `json:"name" yaml:"name"`
	Deployment string  `json:"deployment,omitempty" yaml:"deployment,omitempty"`
	Metric     string  `json:"metric" yaml:"metric"`
	Comparison string  `json:"comparison" yaml:"comparison"`
	Threshold  float64 `json:"threshold" yaml:"threshold"`
	ForSeconds int     `json:"for_seconds" yaml:"for_seconds"`
	Enabled    bool    `json:"enabled" yaml:"enabled"`
}

// Validate reports why a rule cannot be used, if it cannot.
func (r AlertRule) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("a rule needs a name")
	}
	if !knownMetric(r.Metric) {
		return fmt.Errorf("unknown metric %q", r.Metric)
	}
	if r.Comparison != ComparisonAbove && r.Comparison != ComparisonBelow {
		return fmt.Errorf("comparison must be %q or %q", ComparisonAbove, ComparisonBelow)
	}
	if r.ForSeconds < 0 {
		return fmt.Errorf("for_seconds cannot be negative")
	}
	return nil
}

func (r AlertRule) forDuration() time.Duration {
	if r.ForSeconds <= 0 {
		return 0
	}
	return time.Duration(r.ForSeconds) * time.Second
}

func knownMetric(name string) bool {
	switch name {
	case MetricCPUUsage, MetricMemoryUsage, MetricMemoryLimit, MetricNetworkRx, MetricNetworkTx:
		return true
	}
	return false
}

// AlertEvent is a rule changing state, which is the only thing worth telling anyone about.
type AlertEvent struct {
	RuleID     string    `json:"rule_id"`
	RuleName   string    `json:"rule_name"`
	Deployment string    `json:"deployment"`
	Container  string    `json:"container"`
	Metric     string    `json:"metric"`
	Value      float64   `json:"value"`
	Threshold  float64   `json:"threshold"`
	Comparison string    `json:"comparison"`
	State      string    `json:"state"`
	At         time.Time `json:"at"`
}

// Message renders the event the way an operator reads it in a notification.
func (e AlertEvent) Message() string {
	where := e.Container
	if e.Deployment != "" {
		where = fmt.Sprintf("%s in %s", e.Container, e.Deployment)
	}
	if e.State == AlertOK {
		return fmt.Sprintf("%s is back to normal: %s is %s.", e.RuleName, where, formatMetricValue(e.Metric, e.Value))
	}
	return fmt.Sprintf("%s: %s is %s, %s %s.",
		e.RuleName, where, formatMetricValue(e.Metric, e.Value), e.Comparison, formatMetricValue(e.Metric, e.Threshold))
}

func formatMetricValue(metric string, v float64) string {
	switch metric {
	case MetricCPUUsage:
		return fmt.Sprintf("%.1f%%", v)
	case MetricMemoryUsage, MetricMemoryLimit, MetricNetworkRx, MetricNetworkTx:
		return formatBytes(v)
	}
	return fmt.Sprintf("%.2f", v)
}

func formatBytes(v float64) string {
	const unit = 1024.0
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for v >= unit && i < len(units)-1 {
		v /= unit
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// seriesState tracks one rule against one container between evaluations.
type seriesState struct {
	since time.Time
	state string
}

// AlertEngine evaluates rules against the latest samples on a timer.
//
// It reports transitions only. A rule that stays breached is one alert, not one every tick:
// an operator who is told the same thing every fifteen seconds stops reading any of it.
type AlertEngine struct {
	store  *Store
	now    func() time.Time
	notify func(AlertEvent)

	mu     sync.Mutex
	rules  []AlertRule
	states map[string]seriesState
	events []AlertEvent
}

// maxAlertEvents bounds retained history the same way recovery events are bounded.
const maxAlertEvents = 500

func NewAlertEngine(store *Store) *AlertEngine {
	return &AlertEngine{
		store:  store,
		now:    time.Now,
		states: map[string]seriesState{},
	}
}

// OnAlert registers the sink for state changes.
func (e *AlertEngine) OnAlert(fn func(AlertEvent)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.notify = fn
}

// SetRules replaces the rule set, forgetting the state of rules that no longer exist.
func (e *AlertEngine) SetRules(rules []AlertRule) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules = rules

	live := map[string]bool{}
	for _, r := range rules {
		live[r.ID] = true
	}
	for k := range e.states {
		if id, _, ok := strings.Cut(k, "\x00"); ok && !live[id] {
			delete(e.states, k)
		}
	}
}

// Rules returns the current rule set.
func (e *AlertEngine) Rules() []AlertRule {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append(make([]AlertRule, 0, len(e.rules)), e.rules...)
}

// Events returns the recorded state changes, most recent last.
func (e *AlertEngine) Events() []AlertEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append(make([]AlertEvent, 0, len(e.events)), e.events...)
}

// Firing lists the rules currently breached, which is what a UI shows as "needs attention".
func (e *AlertEngine) Firing() []AlertEvent {
	e.mu.Lock()
	defer e.mu.Unlock()

	var out []AlertEvent
	for _, ev := range e.events {
		if ev.State == AlertFiring && e.states[stateKey(ev.RuleID, ev.Container)].state == AlertFiring {
			out = append(out, ev)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

func stateKey(ruleID, container string) string { return ruleID + "\x00" + container }

// breached reports whether a value is past the rule's threshold.
func (r AlertRule) breached(v float64) bool {
	if r.Comparison == ComparisonBelow {
		return v < r.Threshold
	}
	return v > r.Threshold
}

// evaluate checks every rule against the latest sample of every matching series.
func (e *AlertEngine) evaluate() {
	latest := e.store.Latest()

	e.mu.Lock()
	rules := e.rules
	notify := e.notify
	now := e.now()
	var fired []AlertEvent

	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		for _, p := range latest {
			if p.Metric != rule.Metric {
				continue
			}
			if rule.Deployment != "" && p.Deployment != rule.Deployment {
				continue
			}

			key := stateKey(rule.ID, p.Container)
			prev := e.states[key]
			ev := AlertEvent{
				RuleID:     rule.ID,
				RuleName:   rule.Name,
				Deployment: p.Deployment,
				Container:  p.Container,
				Metric:     rule.Metric,
				Value:      p.Value,
				Threshold:  rule.Threshold,
				Comparison: rule.Comparison,
				At:         now,
			}

			switch {
			case rule.breached(p.Value):
				switch prev.state {
				case AlertFiring:
					// Already reported; saying it again adds nothing.
				case AlertPending:
					if now.Sub(prev.since) >= rule.forDuration() {
						e.states[key] = seriesState{since: prev.since, state: AlertFiring}
						ev.State = AlertFiring
						fired = append(fired, ev)
					}
				default:
					// Breached for the first time. A rule with no duration fires now;
					// otherwise it has to stay breached to count.
					if rule.forDuration() == 0 {
						e.states[key] = seriesState{since: now, state: AlertFiring}
						ev.State = AlertFiring
						fired = append(fired, ev)
					} else {
						e.states[key] = seriesState{since: now, state: AlertPending}
					}
				}
			default:
				// Recovering is worth hearing about, but only if the breach was reported.
				if prev.state == AlertFiring {
					ev.State = AlertOK
					fired = append(fired, ev)
				}
				delete(e.states, key)
			}
		}
	}

	e.events = append(e.events, fired...)
	if len(e.events) > maxAlertEvents {
		e.events = e.events[len(e.events)-maxAlertEvents:]
	}
	e.mu.Unlock()

	// Outside the lock: a notification goes over the network and evaluation should not
	// hold readers while it does.
	if notify != nil {
		for _, ev := range fired {
			notify(ev)
		}
	}
}

// Run evaluates on each tick until stopped.
func (e *AlertEngine) Run(stop <-chan struct{}, interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			e.evaluate()
		}
	}
}
