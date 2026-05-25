package api

import (
	"testing"

	"github.com/flatrun/agent/pkg/models"
)

func TestProtectedCommandBlockedUsesConfiguredRules(t *testing.T) {
	cfg := &models.ProtectedModeConfig{
		Enabled: true,
		BlockedCommandRules: []models.ProtectedCommandRule{
			{ID: "fresh", Match: "contains", Pattern: "artisan migrate:fresh"},
			{ID: "wipe", Match: "matches", Pattern: `(?i)\b(db:wipe|migrate:reset)\b`},
		},
	}

	tests := []struct {
		name      string
		command   string
		wantBlock bool
		wantRule  string
	}{
		{
			name:      "contains match",
			command:   "php artisan migrate:fresh --seed",
			wantBlock: true,
			wantRule:  "fresh",
		},
		{
			name:      "regex match",
			command:   "php artisan db:wipe",
			wantBlock: true,
			wantRule:  "wipe",
		},
		{
			name:      "allowed command",
			command:   "php artisan migrate --force",
			wantBlock: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocked, rule, err := protectedCommandBlocked(cfg, tt.command)
			if err != nil {
				t.Fatalf("protectedCommandBlocked returned error: %v", err)
			}
			if blocked != tt.wantBlock {
				t.Fatalf("blocked = %v, want %v", blocked, tt.wantBlock)
			}
			if tt.wantRule != "" && (rule == nil || rule.ID != tt.wantRule) {
				t.Fatalf("rule = %#v, want ID %q", rule, tt.wantRule)
			}
		})
	}
}

func TestProtectedActionBlockedHonorsDisableTerminalWithCustomActions(t *testing.T) {
	cfg := &models.ProtectedModeConfig{
		Enabled:         true,
		BlockedActions:  []string{"delete_deployment"},
		DisableTerminal: true,
	}

	if !protectedActionBlocked(cfg, protectedActionTerminal) {
		t.Fatal("expected terminal to be blocked when disable_terminal is enabled")
	}
}

func TestProtectedCommandBlockedSupportsMatchTypes(t *testing.T) {
	tests := []struct {
		match   string
		pattern string
		command string
	}{
		{match: "equals", pattern: "npm run reset", command: "npm run reset"},
		{match: "prefix", pattern: "mysql -e drop", command: "mysql -e drop database app"},
		{match: "suffix", pattern: "--delete-data", command: "app maintenance --delete-data"},
	}

	for _, tt := range tests {
		t.Run(tt.match, func(t *testing.T) {
			cfg := &models.ProtectedModeConfig{
				Enabled: true,
				BlockedCommandRules: []models.ProtectedCommandRule{{
					ID:      tt.match,
					Match:   tt.match,
					Pattern: tt.pattern,
				}},
			}
			blocked, rule, err := protectedCommandBlocked(cfg, tt.command)
			if err != nil {
				t.Fatalf("protectedCommandBlocked returned error: %v", err)
			}
			if !blocked || rule == nil || rule.ID != tt.match {
				t.Fatalf("expected command to be blocked by %q, blocked=%v rule=%#v", tt.match, blocked, rule)
			}
		})
	}
}

func TestValidateProtectedModeConfigRejectsInvalidRules(t *testing.T) {
	tests := []struct {
		name string
		cfg  *models.ProtectedModeConfig
	}{
		{
			name: "missing match",
			cfg: &models.ProtectedModeConfig{
				BlockedCommandRules: []models.ProtectedCommandRule{{Pattern: "drop database"}},
			},
		},
		{
			name: "unknown match",
			cfg: &models.ProtectedModeConfig{
				BlockedCommandRules: []models.ProtectedCommandRule{{Match: "includes", Pattern: "drop database"}},
			},
		},
		{
			name: "invalid regex",
			cfg: &models.ProtectedModeConfig{
				BlockedCommandRules: []models.ProtectedCommandRule{{Match: "matches", Pattern: "["}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateProtectedModeConfig(tt.cfg); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
