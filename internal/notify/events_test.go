package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/events"
)

func TestIncidentsFallsBackWhenStoreCannotOpen(t *testing.T) {
	base := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(base, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	service := NewService(base)
	if incidents := service.Incidents(); len(incidents) != 0 {
		t.Fatalf("incidents = %#v", incidents)
	}
}

func TestPublishSendsOneNotificationForCorrelatedFailure(t *testing.T) {
	service := NewService(t.TempDir())
	if err := service.Save(Config{Targets: []Target{{ID: "email", Name: "Email", URL: "smtp://test", Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	var deliveries []string
	service.send = func(_, message string) error {
		deliveries = append(deliveries, message)
		return nil
	}
	started := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

	_, err := service.Publish(events.Event{Source: "fleet", Type: "node.unavailable", Severity: events.SeverityCritical, Title: "prod2 unavailable", Message: "The node stopped responding.", Scope: events.Scope{Node: "prod2"}, OccurredAt: started})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		_, err = service.Publish(events.Event{Source: "capacity", Type: "deployment.unavailable", Severity: events.SeverityCritical, Title: "Deployment unavailable", Message: "A dependent deployment is unavailable.", Scope: events.Scope{Node: "prod2", Deployment: "app"}, CorrelationKey: "node:prod2", OccurredAt: started.Add(time.Duration(i+1) * 10 * time.Second)})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(deliveries) != 1 {
		t.Fatalf("deliveries = %d", len(deliveries))
	}
	if !strings.Contains(deliveries[0], "prod2 unavailable") {
		t.Fatalf("delivery = %q", deliveries[0])
	}
}

func TestPublishFiltersTargetsByTopicAndNode(t *testing.T) {
	service := NewService(t.TempDir())
	if err := service.Save(Config{Targets: []Target{
		{ID: "prod", URL: "generic+https://prod.example.test", Enabled: true, Topics: []string{"fleet"}, Nodes: []string{"prod2"}},
		{ID: "dev", URL: "generic+https://dev.example.test", Enabled: true, Topics: []string{"fleet"}, Nodes: []string{"dev1"}},
	}}); err != nil {
		t.Fatal(err)
	}
	var targets []string
	service.send = func(target, _ string) error {
		targets = append(targets, target)
		return nil
	}

	_, err := service.Publish(events.Event{Source: "fleet", Type: "node.unavailable", Severity: events.SeverityCritical, Title: "prod2 unavailable", Scope: events.Scope{Node: "prod2"}, OccurredAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || !strings.Contains(targets[0], "prod.example.test") {
		t.Fatalf("targets = %#v", targets)
	}
}

func TestIncidentsSurviveServiceRestart(t *testing.T) {
	basePath := t.TempDir()
	service := NewService(basePath)
	started := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	if _, err := service.Publish(events.Event{Source: "fleet", Type: "node.unavailable", Severity: events.SeverityCritical, Title: "prod2 unavailable", Scope: events.Scope{Node: "prod2"}, OccurredAt: started}); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := NewService(basePath)
	defer restarted.Close()
	incidents := restarted.Incidents()
	if len(incidents) != 1 || incidents[0].CorrelationKey != "node:prod2" {
		t.Fatalf("incidents = %#v", incidents)
	}
	result, err := restarted.Publish(events.Event{Source: "capacity", Type: "deployment.unavailable", Severity: events.SeverityCritical, Title: "app unavailable", Scope: events.Scope{Node: "prod2", Deployment: "app"}, CorrelationKey: "node:prod2", OccurredAt: started.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Incident.EventCount != 2 || result.Notification != events.NotificationNone {
		t.Fatalf("result = %#v", result)
	}
}

func TestPublishUsesMatchingRules(t *testing.T) {
	service := NewService(t.TempDir())
	defer service.Close()
	if err := service.Save(Config{
		Targets: []Target{
			{ID: "email", URL: "generic+https://email.example.test", Enabled: true},
			{ID: "webhook", URL: "generic+https://hook.example.test", Enabled: true},
		},
		Rules: []Rule{{ID: "critical", Enabled: true, Topics: []string{"fleet"}, Severities: []events.Severity{events.SeverityCritical}, TargetIDs: []string{"email"}}},
	}); err != nil {
		t.Fatal(err)
	}
	var targets []string
	service.send = func(target, _ string) error {
		targets = append(targets, target)
		return nil
	}

	_, err := service.Publish(events.Event{Source: "fleet", Type: "node.unavailable", Severity: events.SeverityCritical, Title: "prod2 unavailable", Scope: events.Scope{Node: "prod2"}, OccurredAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || !strings.Contains(targets[0], "email.example.test") {
		t.Fatalf("targets = %#v", targets)
	}
}
