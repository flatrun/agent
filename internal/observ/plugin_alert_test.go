package observ

import (
	"reflect"
	"testing"
	"time"
)

func TestAlertCoreEventKeepsOneIncidentAcrossRecovery(t *testing.T) {
	at := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	firing := alertCoreEvent(AlertEvent{
		RuleID: "memory", RuleName: "High memory", Deployment: "shop", Container: "web",
		State: AlertFiring, At: at, Targets: []string{"ops"},
	}, "Memory is high")
	recovered := alertCoreEvent(AlertEvent{
		RuleID: "memory", RuleName: "High memory", Deployment: "shop", Container: "web",
		State: AlertOK, incidentResolved: true, At: at.Add(time.Minute), Targets: []string{"ops"},
	}, "Memory is normal")

	if firing.CorrelationKey != recovered.CorrelationKey || !recovered.Resolved {
		t.Fatalf("firing = %#v, recovered = %#v", firing, recovered)
	}
	if firing.Scope.Deployment != "shop" || firing.Scope.Container != "web" || !reflect.DeepEqual(firing.TargetIDs, []string{"ops"}) {
		t.Fatalf("event = %#v", firing)
	}
	secondContainer := alertCoreEvent(AlertEvent{
		RuleID: "memory", RuleName: "High memory", Deployment: "shop", Container: "worker",
		State: AlertFiring, At: at, Targets: []string{"ops"},
	}, "Memory is high")
	if secondContainer.CorrelationKey != firing.CorrelationKey {
		t.Fatalf("events from one rule must share an incident: %q != %q", secondContainer.CorrelationKey, firing.CorrelationKey)
	}
}
