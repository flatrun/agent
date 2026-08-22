package events

import (
	"fmt"
	"sync"
	"time"
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

type Scope struct {
	Node       string `json:"node,omitempty"`
	Deployment string `json:"deployment,omitempty"`
	Container  string `json:"container,omitempty"`
}

type Event struct {
	ID             string         `json:"id"`
	Source         string         `json:"source"`
	Type           string         `json:"type"`
	Severity       Severity       `json:"severity"`
	Title          string         `json:"title"`
	Message        string         `json:"message"`
	Scope          Scope          `json:"scope"`
	CorrelationKey string         `json:"correlation_key,omitempty"`
	OccurredAt     time.Time      `json:"occurred_at"`
	Attributes     map[string]any `json:"attributes,omitempty"`
	TargetIDs      []string       `json:"target_ids,omitempty"`
	Resolved       bool           `json:"resolved,omitempty"`
}

type NotificationAction string

const (
	NotificationNone     NotificationAction = "none"
	NotificationOpened   NotificationAction = "opened"
	NotificationUpdated  NotificationAction = "updated"
	NotificationResolved NotificationAction = "resolved"
)

type IngestResult struct {
	Incident     Incident           `json:"incident"`
	Notification NotificationAction `json:"notification"`
}

type Correlator struct {
	mu             sync.Mutex
	incidents      map[string]Incident
	lastNotifiedAt map[string]time.Time
	updateInterval time.Duration
}

func NewCorrelator(updateInterval time.Duration) *Correlator {
	return &Correlator{
		incidents:      make(map[string]Incident),
		lastNotifiedAt: make(map[string]time.Time),
		updateInterval: updateInterval,
	}
}

func (c *Correlator) Ingest(event Event) IngestResult {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := CorrelationKey(event)
	incident, exists := c.incidents[key]
	if exists && incident.Status == IncidentResolved && event.Resolved {
		return IngestResult{Incident: incident, Notification: NotificationNone}
	}
	action := NotificationNone
	if !exists || incident.Status == IncidentResolved {
		incident = Incident{
			ID:             fmt.Sprintf("%s:%d", key, event.OccurredAt.UnixNano()),
			CorrelationKey: key,
			Status:         IncidentOpen,
			Severity:       event.Severity,
			Title:          event.Title,
			FirstEventAt:   event.OccurredAt,
		}
		action = NotificationOpened
	}

	incident.EventCount++
	incident.LastEventAt = event.OccurredAt
	incident.LastEvent = event
	if severityRank(event.Severity) > severityRank(incident.Severity) {
		incident.Severity = event.Severity
	}
	if event.Resolved {
		incident.Status = IncidentResolved
		action = NotificationResolved
	} else if action == NotificationNone && c.updateInterval > 0 && event.OccurredAt.Sub(c.lastNotifiedAt[key]) >= c.updateInterval {
		action = NotificationUpdated
	}

	c.incidents[key] = incident
	if action != NotificationNone {
		c.lastNotifiedAt[key] = event.OccurredAt
	}
	return IngestResult{Incident: incident, Notification: action}
}

func (c *Correlator) List() []Incident {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]Incident, 0, len(c.incidents))
	for _, incident := range c.incidents {
		result = append(result, incident)
	}
	return result
}

func (c *Correlator) Restore(incidents []Incident) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, incident := range incidents {
		c.incidents[incident.CorrelationKey] = incident
		c.lastNotifiedAt[incident.CorrelationKey] = incident.LastEventAt
	}
}

func severityRank(severity Severity) int {
	switch severity {
	case SeverityCritical:
		return 3
	case SeverityWarning:
		return 2
	default:
		return 1
	}
}

type IncidentStatus string

const (
	IncidentOpen     IncidentStatus = "open"
	IncidentResolved IncidentStatus = "resolved"
)

type Incident struct {
	ID             string         `json:"id"`
	CorrelationKey string         `json:"correlation_key"`
	Status         IncidentStatus `json:"status"`
	Severity       Severity       `json:"severity"`
	Title          string         `json:"title"`
	EventCount     int            `json:"event_count"`
	FirstEventAt   time.Time      `json:"first_event_at"`
	LastEventAt    time.Time      `json:"last_event_at"`
	LastEvent      Event          `json:"last_event"`
}

func CorrelationKey(event Event) string {
	if event.CorrelationKey != "" {
		return event.CorrelationKey
	}
	if event.Scope.Node != "" {
		return "node:" + event.Scope.Node
	}
	if event.Scope.Deployment != "" {
		return "deployment:" + event.Scope.Deployment
	}
	return event.Source + ":" + event.Type
}
