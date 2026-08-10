package observ

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// The rule that would cost the most is the one a user writes without thinking, so the
// defaults have to be the safe ones.
func TestLogRuleDefaultsAreConservative(t *testing.T) {
	r := LogRule{Name: "anything", Deployment: "shop"}.WithDefaults()

	if r.MinLevel != LogLevelError {
		t.Errorf("a rule should watch errors unless told otherwise, got %q", r.MinLevel)
	}
	if r.MinCount < 2 {
		t.Errorf("a single occurrence should not be an incident, got min_count %d", r.MinCount)
	}
	if r.CooldownSeconds < 600 {
		t.Errorf("the quiet period should be long enough to matter, got %ds", r.CooldownSeconds)
	}
	if r.Triage {
		t.Errorf("triage must be opt-in, or writing a rule spends money by accident")
	}
	if len(r.Responders) != 1 || r.Responders[0] != ResponderNotify {
		t.Errorf("a rule should notify by default, got %v", r.Responders)
	}
}

// A rule watching warnings with no pattern matches most of what a chatty app writes.
func TestLogRuleRejectsBroadLowLevelRules(t *testing.T) {
	err := LogRule{Name: "everything", Deployment: "shop", MinLevel: LogLevelWarn}.Validate()
	if err == nil {
		t.Fatal("a warn-level rule with no pattern should be refused")
	}
	if !strings.Contains(err.Error(), "pattern") {
		t.Errorf("the refusal should say what would fix it, got %q", err)
	}

	ok := LogRule{Name: "oom warnings", Deployment: "shop", MinLevel: LogLevelWarn, Pattern: "out of memory"}.Validate()
	if ok != nil {
		t.Errorf("a warn-level rule with a pattern is fine, got %v", ok)
	}
}

func TestLogRuleValidation(t *testing.T) {
	cases := []struct {
		name string
		rule LogRule
		want string
	}{
		{"no name", LogRule{Deployment: "shop"}, "name"},
		{"no deployment", LogRule{Name: "x"}, "deployment"},
		{"bad level", LogRule{Name: "x", Deployment: "shop", MinLevel: "loud"}, "level"},
		{"bad pattern", LogRule{Name: "x", Deployment: "shop", Pattern: "("}, "regular expression"},
		{"unknown responder", LogRule{Name: "x", Deployment: "shop", Responders: []string{"telepathy"}}, "responder"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.rule.Validate()
			if err == nil {
				t.Fatalf("expected a refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal should mention %q, got %q", tc.want, err)
			}
		})
	}
}

// Notify is always valid so a rule written today keeps working after a responder is added or
// removed from a build.
func TestNotifyResponderIsAlwaysAvailable(t *testing.T) {
	if err := (LogRule{Name: "x", Deployment: "shop", Responders: []string{ResponderNotify}}).Validate(); err != nil {
		t.Errorf("notify should always validate, got %v", err)
	}
}

// The framework has to carry a new kind of response without the engine knowing about it.
func TestResponderRegistryRunsWhatARuleAsksFor(t *testing.T) {
	var seen []string
	RegisterResponder(ResponderFunc{
		ResponderName: "test-issue-filer",
		Fn: func(_ context.Context, incident Incident) (string, error) {
			seen = append(seen, incident.Key())
			return "filed issue #1", nil
		},
	})
	t.Cleanup(func() {
		registry.mu.Lock()
		delete(registry.m, "test-issue-filer")
		registry.mu.Unlock()
	})

	if err := (LogRule{Name: "x", Deployment: "shop", Responders: []string{"test-issue-filer"}}).Validate(); err != nil {
		t.Fatalf("a registered responder should validate, got %v", err)
	}

	incident := Incident{RuleID: "r1", Deployment: "shop", Fingerprint: "abc"}
	results := runResponders(context.Background(), []string{"test-issue-filer"}, incident)
	if len(results) != 1 || results[0].Detail != "filed issue #1" {
		t.Fatalf("the responder's outcome should be recorded, got %+v", results)
	}
	if len(seen) != 1 || seen[0] != "shop/r1/abc" {
		t.Errorf("a responder should be handed a stable key for the fault, got %v", seen)
	}
}

// One responder failing must not stop the rest: an incident that could not be filed still has
// to reach a person.
func TestRespondersAreIndependent(t *testing.T) {
	RegisterResponder(ResponderFunc{
		ResponderName: "test-broken",
		Fn:            func(context.Context, Incident) (string, error) { return "", fmt.Errorf("service down") },
	})
	var notified bool
	RegisterResponder(ResponderFunc{
		ResponderName: "test-ok",
		Fn:            func(context.Context, Incident) (string, error) { notified = true; return "done", nil },
	})
	t.Cleanup(func() {
		registry.mu.Lock()
		delete(registry.m, "test-broken")
		delete(registry.m, "test-ok")
		registry.mu.Unlock()
	})

	results := runResponders(context.Background(), []string{"test-broken", "test-ok"}, Incident{})
	if len(results) != 2 {
		t.Fatalf("every responder should report, got %d", len(results))
	}
	if results[0].Error == "" {
		t.Errorf("the failure should be recorded")
	}
	if !notified {
		t.Errorf("a failing responder must not stop the ones after it")
	}
}

// A rule naming a responder this build does not have is recorded, not fatal.
func TestUnknownResponderIsRecordedNotFatal(t *testing.T) {
	results := runResponders(context.Background(), []string{"not-built"}, Incident{})
	if len(results) != 1 || results[0].Error == "" {
		t.Fatalf("expected one recorded failure, got %+v", results)
	}
}

func TestFingerprintCollapsesTheVaryingParts(t *testing.T) {
	same := []string{
		`failed to insert order id=48291 at 2026-08-06T12:00:31.123Z after 14ms`,
		`failed to insert order id=99999 at 2026-08-07T03:11:02.500Z after 9ms`,
	}
	if fingerprint(same[0]) != fingerprint(same[1]) {
		t.Errorf("the same fault with different ids and times should share a fingerprint")
	}

	if fingerprint("out of memory") == fingerprint("deadlock detected") {
		t.Errorf("different faults should not share a fingerprint")
	}
}
