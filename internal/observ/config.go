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
	// RetentionDays bounds how far back stored history goes. Samples older than the recent
	// window are averaged into one point a minute, so a long retention is cheap.
	RetentionDays int `yaml:"retention_days" json:"retention_days"`
	// OTLPEndpoint is where metrics are pushed, if anywhere. An http or https URL speaks
	// OTLP/HTTP; a bare host:port speaks OTLP/gRPC. Left empty, the standard
	// OTEL_EXPORTER_OTLP_ENDPOINT environment variable is honoured instead, and with
	// neither set nothing is pushed and the metrics are still there to scrape.
	OTLPEndpoint string `yaml:"otlp_endpoint,omitempty" json:"otlp_endpoint,omitempty"`
	// Off unless turned on here, and still opt-in per rule after that.
	LogTriage bool `yaml:"log_triage" json:"log_triage"`
	// Bounds what an incident carries, and so the most a triage can be asked to read.
	TriageContextLines int `yaml:"triage_context_lines,omitempty" json:"triage_context_lines,omitempty"`
}

func (c Config) triageContextLines() int {
	if c.TriageContextLines <= 0 {
		return 12
	}
	if c.TriageContextLines > maxLogContextLines {
		return maxLogContextLines
	}
	return c.TriageContextLines
}

// DefaultConfig returns the built-in defaults.
func DefaultConfig() Config {
	return Config{SampleIntervalSeconds: 5, AutoRestart: true, RestartCooldownSeconds: 120, RetentionDays: 7}
}

func (c Config) retention() time.Duration {
	if c.RetentionDays <= 0 {
		return rollupWindow
	}
	return time.Duration(c.RetentionDays) * 24 * time.Hour
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
	"retention_days":           map[string]any{"type": "number", "label": "Keep history for (days)", "default": 7, "min": 1},
	"otlp_endpoint":            map[string]any{"type": "string", "label": "OTLP endpoint", "placeholder": "http://localhost:4318", "help": "Push metrics to an OpenTelemetry backend. Leave empty to only serve them for scraping."},
	"log_triage":               map[string]any{"type": "boolean", "label": "Let log rules ask the assistant", "default": false, "help": "Log rules that opt in can have the assistant explain an incident. Bounded by the agent's daily triage cap."},
	"triage_context_lines":     map[string]any{"type": "number", "label": "Lines of context per incident", "default": 12, "min": 1, "max": 40},
}
