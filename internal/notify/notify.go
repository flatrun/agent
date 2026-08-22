// Package notify is FlatRun's core notification service. It stores delivery targets (email,
// webhook, chat, ... as shoutrrr URLs), routes events to the enabled ones, and can send a
// test message. It is a core capability: plugins emit events and the core delivers them, so
// notification configuration lives in one place rather than per plugin.
package notify

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flatrun/agent/internal/events"
	"github.com/nicholas-fedor/shoutrrr"
	"github.com/nicholas-fedor/shoutrrr/pkg/router"
	"gopkg.in/yaml.v3"
)

// MaskedURL stands in for a target URL in API responses. A shoutrrr URL carries
// its credentials inline (an SMTP password, a webhook token), so the real URL is
// never returned to a client; it is written verbatim only to the on-disk store.
const MaskedURL = "********"

// Target is one delivery destination. URL is a shoutrrr service URL, e.g.
// "smtp://user:pass@host:587/?from=x&to=y" or "generic+https://example.com/hook".
type Target struct {
	ID          string            `yaml:"id" json:"id"`
	Name        string            `yaml:"name" json:"name"`
	URL         string            `yaml:"url" json:"url"`
	Enabled     bool              `yaml:"enabled" json:"enabled"`
	Topics      []string          `yaml:"topics,omitempty" json:"topics,omitempty"`
	Severities  []events.Severity `yaml:"severities,omitempty" json:"severities,omitempty"`
	Nodes       []string          `yaml:"nodes,omitempty" json:"nodes,omitempty"`
	Deployments []string          `yaml:"deployments,omitempty" json:"deployments,omitempty"`
}

// MarshalJSON masks the credential-bearing URL. YAML persistence does not use
// this path, so the stored file keeps the real URL.
func (t Target) MarshalJSON() ([]byte, error) {
	type alias Target
	masked := alias(t)
	if masked.URL != "" {
		masked.URL = MaskedURL
	}
	return json.Marshal(masked)
}

// Config is the persisted notification settings.
type Config struct {
	Targets []Target `yaml:"targets" json:"targets"`
}

// Service loads/saves targets and delivers messages.
type Service struct {
	path   string
	mu     sync.RWMutex
	send   func(url, message string) error // overridable in tests
	events *events.Correlator
}

func NewService(basePath string) *Service {
	return &Service{
		path:   filepath.Join(basePath, ".flatrun", "notifications.yml"),
		events: events.NewCorrelator(15 * time.Minute),
	}
}

func (s *Service) Publish(event events.Event) (events.IngestResult, error) {
	result := s.events.Ingest(event)
	if result.Notification == events.NotificationNone {
		return result, nil
	}

	kind := KindNegative
	message := event.Message
	switch result.Notification {
	case events.NotificationResolved:
		kind = KindPositive
		message = fmt.Sprintf("Resolved after %d related events. %s", result.Incident.EventCount, event.Message)
	case events.NotificationUpdated:
		message = fmt.Sprintf("%d related events are grouped in this incident. %s", result.Incident.EventCount, event.Message)
	}
	if event.Severity == events.SeverityInfo && result.Notification != events.NotificationResolved {
		kind = KindGeneric
	}

	notification := Notification{
		Kind:    kind,
		Title:   event.Title,
		Message: message,
		Panels: []Panel{{
			Title:  "Incident ID",
			Value:  result.Incident.ID,
			Detail: fmt.Sprintf("Source: %s", event.Source),
		}},
	}
	return result, s.deliverEvent(event, notification)
}

func (s *Service) Incidents() []events.Incident {
	return s.events.List()
}

func (s *Service) deliverEvent(event events.Event, notification Notification) error {
	cfg := s.Load()
	var firstErr error
	for _, target := range cfg.Targets {
		if !target.Enabled || target.URL == "" || !targetMatches(target, event) {
			continue
		}
		if err := s.deliver(target.URL, notification); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func targetMatches(target Target, event events.Event) bool {
	return matchesString(target.Topics, event.Source) &&
		matchesSeverity(target.Severities, event.Severity) &&
		matchesString(target.Nodes, event.Scope.Node) &&
		matchesString(target.Deployments, event.Scope.Deployment)
}

func matchesString(filter []string, value string) bool {
	if len(filter) == 0 {
		return true
	}
	for _, candidate := range filter {
		if candidate == value {
			return true
		}
	}
	return false
}

func matchesSeverity(filter []events.Severity, value events.Severity) bool {
	if len(filter) == 0 {
		return true
	}
	for _, candidate := range filter {
		if candidate == value {
			return true
		}
	}
	return false
}

func (s *Service) Load() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var cfg Config
	data, err := os.ReadFile(s.path)
	if err != nil {
		return cfg
	}
	_ = yaml.Unmarshal(data, &cfg)
	return cfg
}

func (s *Service) Save(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}

// Update saves targets, restoring the stored URL for any target whose incoming
// URL is the mask: a client that received a masked target and saved it back
// unchanged must not overwrite the real URL with the mask.
func (s *Service) Update(cfg Config) error {
	stored := s.Load()
	byID := make(map[string]string, len(stored.Targets))
	for _, t := range stored.Targets {
		byID[t.ID] = t.URL
	}
	for i := range cfg.Targets {
		if cfg.Targets[i].URL == MaskedURL {
			cfg.Targets[i].URL = byID[cfg.Targets[i].ID]
		}
	}
	return s.Save(cfg)
}

// Test sends a message to a single URL, so an admin can verify a target before saving it.
func (s *Service) Test(url string) error {
	if url == "" {
		return fmt.Errorf("no target url")
	}
	return s.deliver(url, Notification{Title: "FlatRun test notification", Message: "Your target is configured correctly."})
}

// Notify delivers title + message to every enabled target. It returns the first delivery
// error, if any, but attempts all targets.
func (s *Service) Notify(title, message string) error {
	return s.NotifyTargets(title, message, nil)
}

// NotifyTargets delivers to a chosen subset of targets by id. An empty list
// means every enabled target, which is what plain Notify does.
func (s *Service) NotifyTargets(title, message string, ids []string) error {
	return s.NotifyNotificationTargets(Notification{Title: title, Message: message}, ids)
}

func (s *Service) NotifyNotificationTargets(notification Notification, ids []string) error {
	cfg := s.Load()
	var only map[string]bool
	if len(ids) > 0 {
		only = make(map[string]bool, len(ids))
		for _, id := range ids {
			only[id] = true
		}
	}
	var firstErr error
	for _, t := range cfg.Targets {
		if !t.Enabled || t.URL == "" {
			continue
		}
		if only != nil && !only[t.ID] {
			continue
		}
		if err := s.deliver(t.URL, notification); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Service) deliver(rawURL string, notification Notification) error {
	targetURL, body, err := formatDelivery(rawURL, notification)
	if err != nil {
		return err
	}
	if s.send == nil {
		return sendFormatted(targetURL, notification)
	}
	return s.send(targetURL, body)
}

func sendFormatted(rawURL string, notification Notification) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "smtp" {
		return shoutrrr.Send(rawURL, plainMessage(notification))
	}
	return sendEmail(rawURL, notification, &router.ServiceRouter{})
}

func plainMessage(notification Notification) string {
	var body strings.Builder
	if notification.Title != "" {
		body.WriteString(notification.Title)
		body.WriteString("\n\n")
	}
	body.WriteString(notification.Message)
	for _, panel := range notification.Panels {
		body.WriteString("\n\n")
		body.WriteString(panel.Title)
		if panel.Value != "" {
			body.WriteString(": ")
			body.WriteString(panel.Value)
		}
		if panel.Detail != "" {
			body.WriteString("\n")
			body.WriteString(panel.Detail)
		}
	}
	return body.String()
}
