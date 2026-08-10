package observ

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testRule(mutate func(*LogRule)) LogRule {
	r := LogRule{
		ID:         "rule-1",
		Name:       "App errors",
		Enabled:    true,
		Deployment: "shop",
	}
	if mutate != nil {
		mutate(&r)
	}
	return r.WithDefaults()
}

func line(at time.Time, level, message string) LogLine {
	return LogLine{
		Deployment: "shop",
		Service:    "web",
		Source:     "stdout",
		Level:      level,
		Message:    message,
		Raw:        "shop-web | " + message,
		At:         at,
	}
}

// A single error is a blip. Raising an incident for it would page an operator for every
// transient failure a healthy system produces.
func TestLogEngineWaitsForTheBurstThreshold(t *testing.T) {
	e := NewLogEngine()
	e.SetRules([]LogRule{testRule(nil)})

	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	if got := e.Offer(line(base, "error", "connection refused talking to redis")); len(got) != 0 {
		t.Fatalf("one occurrence should not raise anything, got %d", len(got))
	}
	if got := e.Offer(line(base.Add(time.Second), "error", "connection refused talking to redis")); len(got) != 0 {
		t.Fatalf("two occurrences should not raise anything, got %d", len(got))
	}

	raised := e.Offer(line(base.Add(2*time.Second), "error", "connection refused talking to redis"))
	if len(raised) != 1 {
		t.Fatalf("the third occurrence should raise one incident, got %d", len(raised))
	}
	if raised[0].Count != 3 {
		t.Errorf("incident should carry the occurrence count, got %d", raised[0].Count)
	}
}

// The gate that decides whether this feature is affordable: one fault repeated forever is one
// incident, however many lines it writes.
func TestLogEngineRaisesOneIncidentPerFaultPerCooldown(t *testing.T) {
	e := NewLogEngine()
	e.SetRules([]LogRule{testRule(func(r *LogRule) {
		r.MinCount = 3
		r.CooldownSeconds = 3600
	})})

	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	raisedTotal := 0
	// The same fault, with the varying parts real logs carry, ten thousand times inside one
	// cooldown period.
	for i := 0; i < 10000; i++ {
		msg := fmt.Sprintf("failed to insert order id=%d at 2026-08-06T12:00:%02d.123Z after %dms", 40000+i, i%60, 12+i%7)
		raisedTotal += len(e.Offer(line(base.Add(time.Duration(i)*100*time.Millisecond), "error", msg)))
	}

	if raisedTotal != 1 {
		t.Fatalf("ten thousand copies of one fault should raise one incident, got %d", raisedTotal)
	}

	incidents := e.Incidents()
	if len(incidents) != 1 {
		t.Fatalf("expected one incident in history, got %d", len(incidents))
	}
	if incidents[0].Count < 1000 {
		t.Errorf("the repeats should be counted onto the open incident, got %d", incidents[0].Count)
	}
}

// Distinct faults are distinct incidents, or the collapsing would hide the second problem.
func TestLogEngineSeparatesDistinctFaults(t *testing.T) {
	e := NewLogEngine()
	e.SetRules([]LogRule{testRule(func(r *LogRule) { r.MinCount = 2 })})

	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	var raised []Incident
	for i := 0; i < 2; i++ {
		raised = append(raised, e.Offer(line(base.Add(time.Duration(i)*time.Second), "error", "out of memory killing worker 12"))...)
	}
	for i := 0; i < 2; i++ {
		raised = append(raised, e.Offer(line(base.Add(time.Duration(10+i)*time.Second), "error", "deadlock detected on table orders"))...)
	}

	if len(raised) != 2 {
		t.Fatalf("two different faults should raise two incidents, got %d", len(raised))
	}
	if raised[0].Fingerprint == raised[1].Fingerprint {
		t.Errorf("different faults should not share a fingerprint")
	}
}

// Once the cooldown passes the same fault is worth mentioning again: it means it never went
// away, or it came back.
func TestLogEngineRaisesAgainAfterTheCooldown(t *testing.T) {
	e := NewLogEngine()
	e.SetRules([]LogRule{testRule(func(r *LogRule) {
		r.MinCount = 1
		r.CooldownSeconds = 60
	})})

	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	if got := e.Offer(line(base, "error", "disk full")); len(got) != 1 {
		t.Fatalf("expected the first incident, got %d", len(got))
	}
	if got := e.Offer(line(base.Add(30*time.Second), "error", "disk full")); len(got) != 0 {
		t.Fatalf("inside the cooldown nothing should be raised, got %d", len(got))
	}
	if got := e.Offer(line(base.Add(90*time.Second), "error", "disk full")); len(got) != 1 {
		t.Fatalf("after the cooldown it should raise again, got %d", len(got))
	}
}

func TestLogEngineGatesOnLevelAndPattern(t *testing.T) {
	e := NewLogEngine()
	e.SetRules([]LogRule{testRule(func(r *LogRule) {
		r.MinCount = 1
		r.Pattern = "(?i)out of memory"
	})})

	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	if got := e.Offer(line(base, "warn", "out of memory soon")); len(got) != 0 {
		t.Errorf("a warning should not match a rule watching errors")
	}
	if got := e.Offer(line(base.Add(time.Second), "error", "connection reset")); len(got) != 0 {
		t.Errorf("an error not matching the pattern should not raise")
	}
	if got := e.Offer(line(base.Add(2*time.Second), "error", "Out Of Memory: killed")); len(got) != 1 {
		t.Errorf("an error matching the pattern should raise, got %d", len(got))
	}
}

// A line whose level could not be parsed must not slip past a rule that asked for errors.
func TestLogEngineIgnoresUnparseableLevels(t *testing.T) {
	e := NewLogEngine()
	e.SetRules([]LogRule{testRule(func(r *LogRule) { r.MinCount = 1 })})

	if got := e.Offer(line(time.Now().UTC(), "", "something happened")); len(got) != 0 {
		t.Errorf("a line with no level should not match an error rule")
	}
}

func TestLogEngineScopesToDeploymentAndService(t *testing.T) {
	e := NewLogEngine()
	e.SetRules([]LogRule{testRule(func(r *LogRule) {
		r.MinCount = 1
		r.Service = "worker"
	})})

	at := time.Now().UTC()
	other := line(at, "error", "boom")
	other.Deployment = "blog"
	if got := e.Offer(other); len(got) != 0 {
		t.Errorf("another deployment's line should not match")
	}

	wrongService := line(at, "error", "boom")
	if got := e.Offer(wrongService); len(got) != 0 {
		t.Errorf("another service's line should not match")
	}

	right := line(at, "error", "boom")
	right.Service = "worker"
	if got := e.Offer(right); len(got) != 1 {
		t.Errorf("the watched service should match, got %d", len(got))
	}
}

// The agent runs for months. Both of the engine's maps are keyed by things that come and go, a
// stream by its service and a window by the shape of a message, so neither may grow forever.
func TestLogEngineForgetsStreamsAndShapesItNoLongerNeeds(t *testing.T) {
	e := NewLogEngine()
	e.SetRules([]LogRule{testRule(func(r *LogRule) {
		r.MinCount = 1
		r.WindowSeconds = 60
		r.CooldownSeconds = 60
	})})

	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 500; i++ {
		l := line(base.Add(time.Duration(i)*time.Millisecond), "error", fmt.Sprintf("connection refused talking to shard %d", i))
		l.Service = fmt.Sprintf("worker-%d", i)
		e.Offer(l)
	}

	e.mu.Lock()
	windows, streams := len(e.windows), len(e.recentLines)
	e.mu.Unlock()
	if windows == 0 || streams == 0 {
		t.Fatalf("expected the engine to be holding state, got %d windows and %d streams", windows, streams)
	}

	// A day later, none of it is still deciding anything.
	quiet := line(base.Add(24*time.Hour), "info", "still here")
	quiet.Service = "web"
	e.Offer(quiet)

	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.windows) != 0 {
		t.Errorf("windows past their cooldown should be dropped, %d left", len(e.windows))
	}
	if len(e.recentLines) != 1 {
		t.Errorf("only the stream still writing should be kept, %d left", len(e.recentLines))
	}
}

// Triage is the only part that costs money, so it must run for the incident and never per
// line, and only when the rule asked for it.
func TestLogEngineTriagesOncePerIncidentAndOnlyWhenAsked(t *testing.T) {
	var calls int64
	e := NewLogEngine()
	e.OnTriage(func(_ context.Context, incident Incident) (*Triage, error) {
		atomic.AddInt64(&calls, 1)
		return &Triage{Summary: "redis is down"}, nil
	})
	e.SetRules([]LogRule{
		testRule(func(r *LogRule) {
			r.ID = "no-triage"
			r.MinCount = 1
			r.Triage = false
		}),
	})

	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 50; i++ {
		e.Offer(line(base.Add(time.Duration(i)*time.Second), "error", "connection refused"))
	}
	if got := atomic.LoadInt64(&calls); got != 0 {
		t.Fatalf("a rule without triage must never reach the model, got %d calls", got)
	}

	e2 := NewLogEngine()
	e2.OnTriage(func(_ context.Context, incident Incident) (*Triage, error) {
		atomic.AddInt64(&calls, 1)
		return &Triage{Summary: "redis is down"}, nil
	})
	e2.SetRules([]LogRule{testRule(func(r *LogRule) {
		r.MinCount = 1
		r.CooldownSeconds = 3600
		r.Triage = true
	})})

	var raised []Incident
	for i := 0; i < 500; i++ {
		raised = append(raised, e2.Offer(line(base.Add(time.Duration(i)*time.Second), "error", "connection refused"))...)
	}
	if got := atomic.LoadInt64(&calls); got != 0 {
		t.Fatalf("reading a line must never reach the model on its own, got %d calls", got)
	}
	if len(raised) != 1 {
		t.Fatalf("five hundred lines of one fault should raise one incident, got %d", len(raised))
	}

	verdict := e2.Explain(raised[0])
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("five hundred lines of one fault should cost one triage, got %d", got)
	}
	if verdict == nil || verdict.Summary != "redis is down" {
		t.Fatalf("the incident should carry the verdict, got %+v", verdict)
	}
}

// A triage that fails leaves the incident intact: the operator still hears about the fault.
func TestLogEngineRaisesWhenTriageFails(t *testing.T) {
	e := NewLogEngine()
	e.OnTriage(func(_ context.Context, _ Incident) (*Triage, error) {
		return nil, fmt.Errorf("daily triage budget spent")
	})
	e.SetRules([]LogRule{testRule(func(r *LogRule) {
		r.MinCount = 1
		r.Triage = true
	})})

	raised := e.Offer(line(time.Now().UTC(), "error", "everything is on fire"))
	if len(raised) != 1 {
		t.Fatalf("expected the incident regardless of triage, got %d", len(raised))
	}
	verdict := e.Explain(raised[0])
	if verdict == nil || !strings.Contains(verdict.Skipped, "budget") {
		t.Errorf("the incident should record why it was not triaged, got %+v", verdict)
	}
}

func TestLogEngineCarriesContextForTriage(t *testing.T) {
	e := NewLogEngine()
	e.SetContextLines(5)
	e.SetRules([]LogRule{testRule(func(r *LogRule) { r.MinCount = 1 })})

	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		e.Offer(line(base.Add(time.Duration(i)*time.Millisecond), "info", fmt.Sprintf("handling request %d", i)))
	}
	raised := e.Offer(line(base.Add(time.Second), "error", "panic: nil map write"))
	if len(raised) != 1 {
		t.Fatalf("expected one incident, got %d", len(raised))
	}
	if len(raised[0].Context) != 5 {
		t.Fatalf("context should be bounded to what was asked for, got %d lines", len(raised[0].Context))
	}
	if !strings.Contains(raised[0].Context[len(raised[0].Context)-1], "panic") {
		t.Errorf("the matched line should be the last of the context, got %q", raised[0].Context[len(raised[0].Context)-1])
	}
}
