package config

import "testing"

func TestTemplatesSyncIntervalDefault(t *testing.T) {
	var cfg Config
	setDefaults(&cfg)
	if cfg.Templates.SyncInterval == nil || *cfg.Templates.SyncInterval != 3600 {
		t.Fatalf("unset sync_interval should default to 3600, got %v", cfg.Templates.SyncInterval)
	}
}

func TestTemplatesSyncIntervalZeroDisablesAndSurvives(t *testing.T) {
	zero := 0
	cfg := Config{Templates: TemplatesConfig{SyncInterval: &zero}}
	setDefaults(&cfg)
	if cfg.Templates.SyncInterval == nil || *cfg.Templates.SyncInterval != 0 {
		t.Fatalf("an explicit 0 must be preserved so the resync loop can be disabled, got %v", cfg.Templates.SyncInterval)
	}
}

func TestTemplatesGitHubDefaults(t *testing.T) {
	var cfg Config
	setDefaults(&cfg)
	if cfg.Templates.GitHub.Enabled == nil || !*cfg.Templates.GitHub.Enabled {
		t.Error("github source should default to enabled")
	}
	if cfg.Templates.GitHub.Repo != "flatrun/templates" || cfg.Templates.GitHub.Ref != "main" {
		t.Errorf("unexpected github defaults: %+v", cfg.Templates.GitHub)
	}
}
