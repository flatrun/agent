// Package notify is FlatRun's core notification service. It stores delivery targets (email,
// webhook, chat, ... as shoutrrr URLs), routes events to the enabled ones, and can send a
// test message. It is a core capability: plugins emit events and the core delivers them, so
// notification configuration lives in one place rather than per plugin.
package notify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/nicholas-fedor/shoutrrr"
	"gopkg.in/yaml.v3"
)

// MaskedURL stands in for a target URL in API responses. A shoutrrr URL carries
// its credentials inline (an SMTP password, a webhook token), so the real URL is
// never returned to a client; it is written verbatim only to the on-disk store.
const MaskedURL = "********"

// Target is one delivery destination. URL is a shoutrrr service URL, e.g.
// "smtp://user:pass@host:587/?from=x&to=y" or "generic+https://example.com/hook".
type Target struct {
	ID      string `yaml:"id" json:"id"`
	Name    string `yaml:"name" json:"name"`
	URL     string `yaml:"url" json:"url"`
	Enabled bool   `yaml:"enabled" json:"enabled"`
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
	path string
	mu   sync.RWMutex
	send func(url, message string) error // overridable in tests
}

func NewService(basePath string) *Service {
	return &Service{
		path: filepath.Join(basePath, ".flatrun", "notifications.yml"),
		send: shoutrrr.Send,
	}
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
	return s.send(url, "FlatRun test notification: your target is configured correctly.")
}

// Notify delivers title + message to every enabled target. It returns the first delivery
// error, if any, but attempts all targets.
func (s *Service) Notify(title, message string) error {
	cfg := s.Load()
	body := message
	if title != "" {
		body = title + "\n\n" + message
	}
	var firstErr error
	for _, t := range cfg.Targets {
		if !t.Enabled || t.URL == "" {
			continue
		}
		if err := s.send(t.URL, body); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
