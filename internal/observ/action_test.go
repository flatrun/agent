package observ

import (
	"testing"
	"time"
)

func TestTopConsumersRanksContainersExcludingHost(t *testing.T) {
	latest := []LatestPoint{
		{SeriesKey: SeriesKey{Deployment: "shop", Container: "web", Metric: MetricMemoryUsage}, Sample: Sample{Value: 300}},
		{SeriesKey: SeriesKey{Deployment: "shop", Container: "db", Metric: MetricMemoryUsage}, Sample: Sample{Value: 900}},
		{SeriesKey: SeriesKey{Container: HostContainer, Metric: MetricMemoryUsage}, Sample: Sample{Value: 9999}},
		{SeriesKey: SeriesKey{Deployment: "blog", Container: "app", Metric: MetricCPUUsage}, Sample: Sample{Value: 50}},
	}
	top := topConsumers(latest, MetricMemoryUsage, 5)
	if len(top) != 2 {
		t.Fatalf("expected 2 container entries (host excluded), got %v", top)
	}
	if top[0].Container != "db" || top[1].Container != "web" {
		t.Errorf("not sorted highest-first: %v", top)
	}
}

func TestHostMemoryAlertSnapshotsContainers(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, store, fired := engineAt(t, &now)
	e.SetRules([]AlertRule{{
		ID: "h1", Name: "Host memory", Metric: MetricHostMemUtil,
		Comparison: ComparisonAbove, Threshold: 90, Enabled: true,
	}})
	store.Record(ContainerSample{Deployment: "shop", Container: "db", MemoryUsage: 900}, now)
	store.Record(ContainerSample{Deployment: "shop", Container: "web", MemoryUsage: 100}, now)
	store.RecordHost(HostSample{MemoryPercent: 95}, now)

	e.evaluate()
	if len(*fired) != 1 {
		t.Fatalf("expected a firing event, got %v", *fired)
	}
	ev := (*fired)[0]
	if len(ev.Snapshot) == 0 || ev.Snapshot[0].Container != "db" {
		t.Errorf("host memory alert should snapshot the top containers by memory: %v", ev.Snapshot)
	}
}

func TestActionRunnerRestartsOnceThenCoolsDown(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var restarted []string
	runner := &ActionRunner{
		restart:  func(dir string) error { restarted = append(restarted, dir); return nil },
		managed:  func(d string) bool { return d == "shop" },
		cooldown: time.Minute,
		dataDir:  "/deploys",
		now:      func() time.Time { return now },
		last:     map[string]time.Time{},
	}

	ev := AlertEvent{State: AlertFiring, Action: ActionRestart, Deployment: "shop", RuleName: "mem"}
	if msg := runner.Run(ev); msg == "" || len(restarted) != 1 {
		t.Fatalf("first firing should restart: msg=%q restarted=%v", msg, restarted)
	}
	// Within the cooldown, a second firing does nothing.
	if msg := runner.Run(ev); msg != "" || len(restarted) != 1 {
		t.Errorf("a restart within the cooldown should be skipped: restarted=%v", restarted)
	}
	// After the cooldown, it restarts again.
	now = now.Add(2 * time.Minute)
	if msg := runner.Run(ev); msg == "" || len(restarted) != 2 {
		t.Errorf("after cooldown it should restart again: restarted=%v", restarted)
	}
}

func TestActionRunnerGuards(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	runner := &ActionRunner{
		restart:  func(string) error { t.Fatal("must not restart"); return nil },
		managed:  func(d string) bool { return d == "shop" },
		cooldown: time.Minute,
		now:      func() time.Time { return now },
		last:     map[string]time.Time{},
	}

	// Not a restart action.
	runner.Run(AlertEvent{State: AlertFiring, Action: ActionNone, Deployment: "shop"})
	// Unmanaged deployment.
	runner.Run(AlertEvent{State: AlertFiring, Action: ActionRestart, Deployment: "stray"})
	// No deployment (a host alert).
	runner.Run(AlertEvent{State: AlertFiring, Action: ActionRestart, Deployment: ""})
}

func TestAlertRuleRejectsUnknownAction(t *testing.T) {
	err := AlertRule{Name: "x", Metric: MetricCPUUsage, Comparison: ComparisonAbove, Action: "nuke"}.Validate()
	if err == nil {
		t.Fatal("an unknown action should be rejected")
	}
}
