package observ

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the observability app's user-tunable settings, stored flat in
// .flatrun/observability.yml.
type Config struct {
	SampleIntervalSeconds  int  `yaml:"sample_interval_seconds" json:"sample_interval_seconds"`
	AutoRestart            bool `yaml:"auto_restart" json:"auto_restart"`
	RestartCooldownSeconds int  `yaml:"restart_cooldown_seconds" json:"restart_cooldown_seconds"`
}

// DefaultConfig returns the built-in defaults.
func DefaultConfig() Config {
	return Config{SampleIntervalSeconds: 5, AutoRestart: true, RestartCooldownSeconds: 120}
}

func (c Config) sampleInterval() time.Duration {
	if c.SampleIntervalSeconds <= 0 {
		return 5 * time.Second
	}
	return time.Duration(c.SampleIntervalSeconds) * time.Second
}

func (c Config) restartCooldown() time.Duration {
	if c.RestartCooldownSeconds <= 0 {
		return 2 * time.Minute
	}
	return time.Duration(c.RestartCooldownSeconds) * time.Second
}

// ConfigStore loads and saves the config from a flat file.
type ConfigStore struct {
	path string
	mu   sync.RWMutex
}

func NewConfigStore(basePath string) *ConfigStore {
	return &ConfigStore{path: filepath.Join(basePath, ".flatrun", "observability.yml")}
}

func (s *ConfigStore) Load() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg := DefaultConfig()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return cfg
	}
	_ = yaml.Unmarshal(data, &cfg)
	return cfg
}

func (s *ConfigStore) Save(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}

// ConfigSchema describes the config for the settings form the UI renders.
var ConfigSchema = map[string]any{
	"sample_interval_seconds":  map[string]any{"type": "number", "label": "Sample interval (seconds)", "default": 5, "min": 1},
	"auto_restart":             map[string]any{"type": "boolean", "label": "Auto-restart unhealthy containers", "default": true},
	"restart_cooldown_seconds": map[string]any{"type": "number", "label": "Restart cooldown (seconds)", "default": 120, "min": 10},
}
