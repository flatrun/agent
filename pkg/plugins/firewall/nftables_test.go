package firewall

import (
	"errors"
	"strings"
	"testing"
)

var errBadRuleset = errors.New("bad ruleset")

type fakeRunner struct {
	available bool
	snapshot  string
	applyErr  error // returned when a full table script is applied
	scripts   []string
}

func (f *fakeRunner) Available() bool              { return f.available }
func (f *fakeRunner) ListRuleset() (string, error) { return f.snapshot, nil }
func (f *fakeRunner) ApplyScript(script string) error {
	f.scripts = append(f.scripts, script)
	if f.applyErr != nil && strings.Contains(script, "table inet flatrun_firewall {") {
		return f.applyErr
	}
	return nil
}

func TestRenderNftablesKeepsSSHAndBaseTrafficUnderDeny(t *testing.T) {
	cfg := &Config{Enabled: true, DefaultInbound: "deny", DefaultOutbound: "allow"}
	script := renderNftables(cfg, sshSession{serverPort: 2222})

	for _, want := range []string{
		"policy drop;",
		`iif "lo" accept`,
		"ct state established,related accept",
		"tcp dport 2222 accept",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("deny-inbound ruleset must contain %q, got:\n%s", want, script)
		}
	}
}

func TestRenderNftablesDefaultsToSSHPort22(t *testing.T) {
	script := renderNftables(&Config{Enabled: true, DefaultInbound: "deny"}, sshSession{})
	if !strings.Contains(script, "tcp dport 22 accept") {
		t.Errorf("without a detected SSH session, port 22 must be kept open, got:\n%s", script)
	}
}

func TestRenderNftablesAllowDefaultIsPermissive(t *testing.T) {
	script := renderNftables(&Config{Enabled: true, DefaultInbound: "allow", DefaultOutbound: "allow"}, sshSession{})
	if strings.Contains(script, "policy drop;") {
		t.Errorf("an allow default must not drop, got:\n%s", script)
	}
	if strings.Count(script, "policy accept;") != 2 {
		t.Errorf("both chains should accept by default, got:\n%s", script)
	}
}

func TestRenderNftablesTranslatesRules(t *testing.T) {
	cfg := &Config{Enabled: true, DefaultInbound: "deny", Rules: []Rule{
		{Direction: "inbound", Action: "allow", Protocol: "tcp", Port: 80},
		{Direction: "inbound", Action: "allow", Port: 53}, // any proto -> tcp + udp
		{Direction: "inbound", Action: "deny", CIDR: "192.168.1.5/32"},
		{Direction: "outbound", Action: "deny", Protocol: "tcp", Port: 25},
	}}
	script := renderNftables(cfg, sshSession{})

	for _, want := range []string{
		"tcp dport 80 accept",
		"tcp dport 53 accept",
		"udp dport 53 accept",
		"ip saddr 192.168.1.5/32 drop",
		"tcp dport 25 drop",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("expected rule %q in:\n%s", want, script)
		}
	}
}

func TestApplyNoOpWhenUnavailable(t *testing.T) {
	r := &fakeRunner{available: false}
	enforced, err := Apply(&Config{Enabled: true, DefaultInbound: "deny"}, r)
	if err != nil || enforced {
		t.Fatalf("without nft, apply must be a no-op: enforced=%v err=%v", enforced, err)
	}
	if len(r.scripts) != 0 {
		t.Errorf("no nft script should run when nft is unavailable, got %v", r.scripts)
	}
}

func TestApplyEnforcesWhenEnabled(t *testing.T) {
	r := &fakeRunner{available: true, snapshot: "table inet other {}"}
	enforced, err := Apply(&Config{Enabled: true, DefaultInbound: "deny"}, r)
	if err != nil || !enforced {
		t.Fatalf("apply should enforce: enforced=%v err=%v", enforced, err)
	}
	last := r.scripts[len(r.scripts)-1]
	if !strings.Contains(last, "table inet flatrun_firewall {") {
		t.Errorf("expected the firewall table to be applied, got:\n%s", last)
	}
}

func TestApplyRestoresSnapshotOnFailure(t *testing.T) {
	r := &fakeRunner{available: true, snapshot: "table inet other { chain c {} }", applyErr: errBadRuleset}
	enforced, err := Apply(&Config{Enabled: true, DefaultInbound: "deny"}, r)
	if err == nil || enforced {
		t.Fatalf("a failed apply must report the error: enforced=%v err=%v", enforced, err)
	}
	restored := false
	for _, s := range r.scripts {
		if strings.Contains(s, "flush ruleset") && strings.Contains(s, "table inet other") {
			restored = true
		}
	}
	if !restored {
		t.Errorf("the previous ruleset should be restored after a failed apply, scripts:\n%v", r.scripts)
	}
}

func TestApplyDisabledRemovesTable(t *testing.T) {
	r := &fakeRunner{available: true}
	enforced, err := Apply(&Config{Enabled: false}, r)
	if err != nil || enforced {
		t.Fatalf("a disabled firewall is not enforced: enforced=%v err=%v", enforced, err)
	}
	if len(r.scripts) != 1 || !strings.Contains(r.scripts[0], "delete table inet flatrun_firewall") {
		t.Errorf("disabling should remove only the firewall table, got %v", r.scripts)
	}
	if strings.Contains(r.scripts[0], "chain input") {
		t.Errorf("disabling should not define chains, got:\n%s", r.scripts[0])
	}
}

func TestDetectSSHSession(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "203.0.113.4 51000 10.0.0.2 2222")
	s := detectSSHSession()
	if s.clientIP != "203.0.113.4" || s.serverPort != 2222 {
		t.Errorf("unexpected parse: %+v", s)
	}

	t.Setenv("SSH_CONNECTION", "")
	if s := detectSSHSession(); s.serverPort != 0 {
		t.Errorf("no SSH_CONNECTION should yield an empty session, got %+v", s)
	}
}
