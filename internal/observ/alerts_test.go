package observ

import (
	"testing"
	"time"
)

func engineAt(t *testing.T, now *time.Time) (*AlertEngine, *Store, *[]AlertEvent) {
	t.Helper()
	store := NewStore(10)
	e := NewAlertEngine(store)
	e.now = func() time.Time { return *now }

	var fired []AlertEvent
	e.OnAlert(func(ev AlertEvent) { fired = append(fired, ev) })
	return e, store, &fired
}

func cpuRule(forSeconds int) AlertRule {
	return AlertRule{
		ID: "r1", Name: "CPU high", Metric: MetricCPUUsage,
		Comparison: ComparisonAbove, Threshold: 80, ForSeconds: forSeconds, Enabled: true,
	}
}

func record(store *Store, cpu float64, at time.Time) {
	store.Record(ContainerSample{Deployment: "shop", Container: "shop-web", CPUPercent: cpu}, at)
}

func TestAlertFiresOnlyAfterItHasHeld(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, store, fired := engineAt(t, &now)
	e.SetRules([]AlertRule{cpuRule(60)})

	// A brief spike is what happens every time a container starts. It must not alert.
	record(store, 95, now)
	e.evaluate()
	if len(*fired) != 0 {
		t.Fatalf("alerted on a momentary spike: %+v", *fired)
	}

	now = now.Add(30 * time.Second)
	record(store, 95, now)
	e.evaluate()
	if len(*fired) != 0 {
		t.Fatalf("alerted before the rule's duration had passed: %+v", *fired)
	}

	now = now.Add(31 * time.Second)
	record(store, 95, now)
	e.evaluate()
	if len(*fired) != 1 {
		t.Fatalf("expected 1 alert once it had held, got %d: %+v", len(*fired), *fired)
	}
	if (*fired)[0].State != AlertFiring {
		t.Errorf("state = %q, want firing", (*fired)[0].State)
	}
}

func TestAlertIsReportedOncePerBreach(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, store, fired := engineAt(t, &now)
	e.SetRules([]AlertRule{cpuRule(0)})

	// Evaluation runs on a timer for as long as it stays breached. Repeating the same
	// alert every tick is how an operator learns to ignore alerts.
	for i := 0; i < 5; i++ {
		record(store, 95, now)
		e.evaluate()
		now = now.Add(15 * time.Second)
	}

	if len(*fired) != 1 {
		t.Errorf("expected 1 alert for a sustained breach, got %d: %+v", len(*fired), *fired)
	}
}

func TestAlertReportsRecovery(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, store, fired := engineAt(t, &now)
	e.SetRules([]AlertRule{cpuRule(0)})

	record(store, 95, now)
	e.evaluate()

	now = now.Add(time.Minute)
	record(store, 10, now)
	e.evaluate()

	if len(*fired) != 2 {
		t.Fatalf("expected a firing and a recovery, got %+v", *fired)
	}
	if (*fired)[1].State != AlertOK {
		t.Errorf("second event = %q, want ok", (*fired)[1].State)
	}

	// Breaching again after recovery is a new alert.
	now = now.Add(time.Minute)
	record(store, 95, now)
	e.evaluate()
	if len(*fired) != 3 {
		t.Errorf("a fresh breach should alert again, got %+v", *fired)
	}
}

func TestAlertDoesNotReportRecoveryItNeverAnnounced(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, store, fired := engineAt(t, &now)
	e.SetRules([]AlertRule{cpuRule(60)})

	// Breached, but recovers before the duration is up: nobody was told it was wrong, so
	// nobody should be told it is better.
	record(store, 95, now)
	e.evaluate()
	now = now.Add(10 * time.Second)
	record(store, 5, now)
	e.evaluate()

	if len(*fired) != 0 {
		t.Errorf("reported a recovery for an alert never sent: %+v", *fired)
	}
}

func TestFiringListsARuleOncePerContainer(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, store, _ := engineAt(t, &now)
	e.SetRules([]AlertRule{cpuRule(0)})

	// Breaking, recovering and breaking again leaves a firing event behind each time. What
	// is firing now is one rule on one container, however many bad days it has had.
	record(store, 95, now)
	e.evaluate()
	now = now.Add(time.Minute)
	record(store, 5, now)
	e.evaluate()
	now = now.Add(time.Minute)
	record(store, 95, now)
	e.evaluate()

	firing := e.Firing()
	if len(firing) != 1 {
		t.Fatalf("got %d entries for one breached rule, want 1: %+v", len(firing), firing)
	}
	// The entry shown is the current breach, not the one from two minutes ago.
	if !firing[0].At.Equal(now) {
		t.Errorf("firing entry is from %s, want the latest breach at %s", firing[0].At, now)
	}

	// And once it recovers it is not firing at all.
	now = now.Add(time.Minute)
	record(store, 5, now)
	e.evaluate()
	if got := e.Firing(); len(got) != 0 {
		t.Errorf("a recovered rule is still listed: %+v", got)
	}
}

func TestAlertBelowThreshold(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, store, fired := engineAt(t, &now)
	e.SetRules([]AlertRule{{
		ID: "r2", Name: "Idle", Metric: MetricCPUUsage,
		Comparison: ComparisonBelow, Threshold: 1, Enabled: true,
	}})

	record(store, 0.5, now)
	e.evaluate()
	if len(*fired) != 1 || (*fired)[0].State != AlertFiring {
		t.Errorf("a below rule did not fire: %+v", *fired)
	}
}

func TestAlertScopedToDeployment(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, store, fired := engineAt(t, &now)

	rule := cpuRule(0)
	rule.Deployment = "other"
	e.SetRules([]AlertRule{rule})

	record(store, 95, now)
	e.evaluate()
	if len(*fired) != 0 {
		t.Errorf("a rule scoped to another deployment fired: %+v", *fired)
	}
}

func TestAlertDisabledRuleNeverFires(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, store, fired := engineAt(t, &now)

	rule := cpuRule(0)
	rule.Enabled = false
	e.SetRules([]AlertRule{rule})

	record(store, 99, now)
	e.evaluate()
	if len(*fired) != 0 {
		t.Errorf("a disabled rule fired: %+v", *fired)
	}
}

func TestAlertRuleValidate(t *testing.T) {
	valid := cpuRule(30)
	if err := valid.Validate(); err != nil {
		t.Errorf("a good rule was rejected: %v", err)
	}

	bad := map[string]AlertRule{
		"no name":         {Metric: MetricCPUUsage, Comparison: ComparisonAbove},
		"unknown metric":  {Name: "x", Metric: "container.disk.usage", Comparison: ComparisonAbove},
		"bad comparison":  {Name: "x", Metric: MetricCPUUsage, Comparison: "equals"},
		"negative window": {Name: "x", Metric: MetricCPUUsage, Comparison: ComparisonAbove, ForSeconds: -1},
	}
	for why, rule := range bad {
		if err := rule.Validate(); err == nil {
			t.Errorf("%s: expected rejection", why)
		}
	}
}

func TestAlertEventMessage(t *testing.T) {
	ev := AlertEvent{
		RuleName: "CPU high", Deployment: "shop", Container: "shop-web",
		Metric: MetricCPUUsage, Value: 93.2, Threshold: 80, Comparison: ComparisonAbove, State: AlertFiring,
	}
	want := "CPU high: shop-web in shop is 93.2%, above 80.0%."
	if got := ev.Message(); got != want {
		t.Errorf("Message() = %q, want %q", got, want)
	}

	mem := AlertEvent{
		RuleName: "Memory", Deployment: "shop", Container: "shop-db",
		Metric: MetricMemoryUsage, Value: 5_368_709_120, Threshold: 4_294_967_296,
		Comparison: ComparisonAbove, State: AlertFiring,
	}
	// Bytes are read as bytes, not as a wall of digits.
	if got := mem.Message(); got != "Memory: shop-db in shop is 5.0 GB, above 4.0 GB." {
		t.Errorf("Message() = %q", got)
	}

	utilization := AlertEvent{
		RuleName: "Memory utilization", Deployment: "shop", Container: "shop-db",
		Metric: MetricMemoryUtilization, Value: 92.5, Threshold: 90,
		Comparison: ComparisonAbove, State: AlertFiring,
	}
	if got := utilization.Message(); got != "Memory utilization: shop-db in shop is 92.5%, above 90.0%." {
		t.Errorf("Message() = %q", got)
	}
}
