// Package firewall is a built-in app modelling a host-wide inbound/outbound firewall.
// Scaffold only: rules are persisted and validated, Apply is a no-op (enforcement via
// nftables/iptables is not wired yet).
package firewall

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	PolicyAllow = "allow"
	PolicyDeny  = "deny"

	DirInbound  = "inbound"
	DirOutbound = "outbound"
)

// Config is the host firewall policy, stored globally in .flatrun/firewall.yml.
type Config struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// DefaultInbound / DefaultOutbound are the stance applied to traffic not matched by a
	// rule: "allow" (default) or "deny".
	DefaultInbound  string `yaml:"default_inbound,omitempty" json:"default_inbound,omitempty"`
	DefaultOutbound string `yaml:"default_outbound,omitempty" json:"default_outbound,omitempty"`
	Rules           []Rule `yaml:"rules,omitempty" json:"rules,omitempty"`
}

type Rule struct {
	ID        string `yaml:"id,omitempty" json:"id,omitempty"`
	Direction string `yaml:"direction" json:"direction"` // inbound | outbound
	Action    string `yaml:"action" json:"action"`       // allow | deny
	Protocol  string `yaml:"protocol,omitempty" json:"protocol,omitempty"`
	Port      int    `yaml:"port,omitempty" json:"port,omitempty"`
	// CIDR is the source for inbound rules or the destination for outbound rules.
	CIDR        string `yaml:"cidr,omitempty" json:"cidr,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// Store loads and saves the host firewall config from a flat file.
type Store struct {
	path string
}

func NewStore(basePath string) *Store {
	return &Store{path: filepath.Join(basePath, ".flatrun", "firewall.yml")}
}

// Load returns the stored config, or a disabled default when no file exists yet.
func (s *Store) Load() (*Config, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid firewall config: %w", err)
	}
	return &cfg, nil
}

func (s *Store) Save(cfg *Config) error {
	if err := Validate(cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}

func validPolicy(p string) bool { return p == "" || p == PolicyAllow || p == PolicyDeny }

// Validate checks the firewall config is well formed before it is saved or applied.
func Validate(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	if !validPolicy(cfg.DefaultInbound) || !validPolicy(cfg.DefaultOutbound) {
		return fmt.Errorf("firewall default policy must be %q or %q", PolicyAllow, PolicyDeny)
	}
	for i, r := range cfg.Rules {
		if r.Direction != DirInbound && r.Direction != DirOutbound {
			return fmt.Errorf("firewall.rules[%d] direction must be %q or %q", i, DirInbound, DirOutbound)
		}
		if r.Action != PolicyAllow && r.Action != PolicyDeny {
			return fmt.Errorf("firewall.rules[%d] action must be %q or %q", i, PolicyAllow, PolicyDeny)
		}
		if r.Protocol != "" && r.Protocol != "tcp" && r.Protocol != "udp" && r.Protocol != "any" {
			return fmt.Errorf("firewall.rules[%d] protocol must be tcp, udp, or any", i)
		}
		if r.Port < 0 || r.Port > 65535 {
			return fmt.Errorf("firewall.rules[%d] port %d out of range", i, r.Port)
		}
		if r.CIDR != "" {
			if _, _, err := net.ParseCIDR(r.CIDR); err != nil {
				return fmt.Errorf("firewall.rules[%d] cidr %q is not valid: %w", i, r.CIDR, err)
			}
		}
	}
	return nil
}

// Plan returns a human-readable description of the rules that would be enforced, so the UI
// and tests can exercise the shape before real enforcement exists.
func Plan(cfg *Config) []string {
	if cfg == nil || !cfg.Enabled {
		return nil
	}
	inbound, outbound := cfg.DefaultInbound, cfg.DefaultOutbound
	if inbound == "" {
		inbound = PolicyAllow
	}
	if outbound == "" {
		outbound = PolicyAllow
	}
	plan := []string{
		fmt.Sprintf("default inbound: %s", inbound),
		fmt.Sprintf("default outbound: %s", outbound),
	}
	for _, r := range cfg.Rules {
		proto := r.Protocol
		if proto == "" {
			proto = "any"
		}
		port := "any"
		if r.Port != 0 {
			port = fmt.Sprintf("%d", r.Port)
		}
		peer := r.CIDR
		if peer == "" {
			peer = "0.0.0.0/0"
		}
		prep := "from"
		if r.Direction == DirOutbound {
			prep = "to"
		}
		plan = append(plan, fmt.Sprintf("%s %s %s/%s %s %s", r.Action, r.Direction, proto, port, prep, peer))
	}
	return plan
}

// nftRunner runs the nft commands enforcement needs. It is an interface so the
// apply/rollback logic can be tested without touching the host firewall.
type nftRunner interface {
	// Available reports whether nft can be used on this host.
	Available() bool
	// ListRuleset returns the current full ruleset, for rollback.
	ListRuleset() (string, error)
	// ApplyScript loads an `nft -f` script.
	ApplyScript(script string) error
}

// Apply enforces the config on the host firewall, or removes FlatRun's rules when
// the firewall is disabled. It reports whether enforcement actually happened:
// when nft is unavailable (a non-Linux host, or nft not installed) the config is
// still saved but not enforced, which is not an error.
//
// Before a new ruleset is loaded the current one is snapshotted, and if the load
// fails the snapshot is restored, so a rejected ruleset never leaves the host in
// a half-applied state. The generated ruleset always keeps loopback,
// established/related, and the active SSH port open, so a default-deny inbound
// policy cannot drop the operator's session.
func Apply(cfg *Config, runner nftRunner) (enforced bool, err error) {
	if err := Validate(cfg); err != nil {
		return false, err
	}
	if runner == nil {
		runner = newExecNftRunner()
	}
	if !runner.Available() {
		return false, nil
	}

	if cfg == nil || !cfg.Enabled {
		// Disabling enforcement removes only FlatRun's table.
		script := fmt.Sprintf("add table inet %s\ndelete table inet %s\n", tableName, tableName)
		if err := runner.ApplyScript(script); err != nil {
			return false, fmt.Errorf("failed to remove firewall rules: %w", err)
		}
		return false, nil
	}

	snapshot, err := runner.ListRuleset()
	if err != nil {
		return false, fmt.Errorf("failed to read current firewall state: %w", err)
	}

	script := renderNftables(cfg, detectSSHSession())
	if err := runner.ApplyScript(script); err != nil {
		if snapshot != "" {
			// Best-effort restore of the ruleset that was in place before.
			_ = runner.ApplyScript("flush ruleset\n" + snapshot)
		}
		return false, fmt.Errorf("failed to apply firewall rules: %w", err)
	}
	return true, nil
}
