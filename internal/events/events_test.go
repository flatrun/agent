package events

import (
	"testing"
	"time"
)

func TestCorrelatorSuppressesDeploymentFloodDuringNodeIncident(t *testing.T) {
	correlator := NewCorrelator(15 * time.Minute)
	started := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	opened := correlator.Ingest(Event{
		Source: "fleet", Type: "node.unavailable", Severity: SeverityCritical,
		Title: "prod2 unavailable", Scope: Scope{Node: "prod2"}, OccurredAt: started,
	})
	if opened.Notification != NotificationOpened {
		t.Fatalf("opened notification = %q", opened.Notification)
	}

	for i := 0; i < 14; i++ {
		result := correlator.Ingest(Event{
			Source: "capacity", Type: "deployment.unavailable", Severity: SeverityCritical,
			Title: "Deployment unavailable", Scope: Scope{Node: "prod2", Deployment: "app"},
			CorrelationKey: "node:prod2", OccurredAt: started.Add(time.Duration(i+1) * 10 * time.Second),
		})
		if result.Notification != NotificationNone {
			t.Fatalf("event %d notification = %q", i, result.Notification)
		}
	}

	incidents := correlator.List()
	if len(incidents) != 1 || incidents[0].EventCount != 15 {
		t.Fatalf("incidents = %#v", incidents)
	}
}

func TestCorrelatorSendsOneRecoveryNotification(t *testing.T) {
	correlator := NewCorrelator(15 * time.Minute)
	started := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	correlator.Ingest(Event{Source: "fleet", Type: "node.unavailable", Severity: SeverityCritical, Title: "prod2 unavailable", Scope: Scope{Node: "prod2"}, OccurredAt: started})

	resolved := correlator.Ingest(Event{Source: "fleet", Type: "node.available", Severity: SeverityInfo, Title: "prod2 recovered", Scope: Scope{Node: "prod2"}, OccurredAt: started.Add(10 * time.Minute), Resolved: true})
	if resolved.Notification != NotificationResolved || resolved.Incident.Status != IncidentResolved {
		t.Fatalf("resolved = %#v", resolved)
	}

	duplicate := correlator.Ingest(Event{Source: "fleet", Type: "node.available", Severity: SeverityInfo, Title: "prod2 recovered", Scope: Scope{Node: "prod2"}, OccurredAt: started.Add(11 * time.Minute), Resolved: true})
	if duplicate.Notification != NotificationNone || duplicate.Incident.ID != resolved.Incident.ID {
		t.Fatalf("duplicate recovery = %#v", duplicate)
	}
}
