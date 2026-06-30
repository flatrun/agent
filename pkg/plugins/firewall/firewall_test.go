package firewall

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		wantErr string
	}{
		{"nil ok", nil, ""},
		{"empty ok", &Config{Enabled: true}, ""},
		{"bad default policy", &Config{DefaultInbound: "drop"}, "default policy"},
		{"bad direction", &Config{Rules: []Rule{{Direction: "sideways", Action: PolicyAllow}}}, "direction"},
		{"bad action", &Config{Rules: []Rule{{Direction: DirInbound, Action: "log"}}}, "action"},
		{"bad protocol", &Config{Rules: []Rule{{Direction: DirInbound, Action: PolicyAllow, Protocol: "icmp"}}}, "protocol"},
		{"bad port", &Config{Rules: []Rule{{Direction: DirInbound, Action: PolicyAllow, Port: 99999}}}, "out of range"},
		{"bad cidr", &Config{Rules: []Rule{{Direction: DirInbound, Action: PolicyAllow, CIDR: "not-a-cidr"}}}, "cidr"},
		{
			"valid",
			&Config{Enabled: true, DefaultInbound: PolicyDeny, Rules: []Rule{{Direction: DirInbound, Action: PolicyAllow, Protocol: "tcp", Port: 22, CIDR: "0.0.0.0/0"}}},
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestPlan(t *testing.T) {
	if Plan(&Config{Enabled: false}) != nil {
		t.Error("Plan(disabled) should be nil")
	}
	cfg := &Config{
		Enabled:        true,
		DefaultInbound: PolicyDeny,
		Rules: []Rule{
			{Direction: DirInbound, Action: PolicyAllow, Protocol: "tcp", Port: 22, CIDR: "0.0.0.0/0"},
			{Direction: DirOutbound, Action: PolicyDeny, CIDR: "10.0.0.0/8"},
		},
	}
	joined := strings.Join(Plan(cfg), "\n")
	if !strings.Contains(joined, "default inbound: deny") || !strings.Contains(joined, "default outbound: allow") {
		t.Errorf("plan should state both default policies, got:\n%s", joined)
	}
	if !strings.Contains(joined, "allow inbound tcp/22 from 0.0.0.0/0") {
		t.Errorf("plan should describe the inbound allow, got:\n%s", joined)
	}
	if !strings.Contains(joined, "deny outbound any/any to 10.0.0.0/8") {
		t.Errorf("plan should describe the outbound deny, got:\n%s", joined)
	}
}

func TestStoreRoundtrip(t *testing.T) {
	base := t.TempDir()
	store := NewStore(base)

	// Loading before anything is saved returns a disabled default, not an error.
	cfg, err := store.Load()
	if err != nil || cfg.Enabled {
		t.Fatalf("Load() before save = (%+v, %v), want disabled default", cfg, err)
	}

	want := &Config{Enabled: true, DefaultInbound: PolicyDeny, Rules: []Rule{{Direction: DirInbound, Action: PolicyAllow, Port: 443}}}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	if _, err := store.Load(); err != nil {
		t.Fatalf("Load() after save = %v", err)
	}
	got, _ := store.Load()
	if !got.Enabled || got.DefaultInbound != PolicyDeny || len(got.Rules) != 1 || got.Rules[0].Port != 443 {
		t.Errorf("round-tripped config = %+v, want %+v", got, want)
	}
	if filepath.Base(store.path) != "firewall.yml" {
		t.Errorf("unexpected store path %q", store.path)
	}
}
