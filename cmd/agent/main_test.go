package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootCmd_BareShowsHelp(t *testing.T) {
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("bare invocation should not error, got: %v", err)
	}

	got := out.String()
	for _, want := range []string{"serve", "setup", "update", "version"} {
		if !strings.Contains(got, want) {
			t.Errorf("help output missing %q command, got:\n%s", want, got)
		}
	}
}

func TestNormalizeLegacyFlags(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"single-dash config", []string{"-config", "/etc/x.yml"}, []string{"--config", "/etc/x.yml"}},
		{"single-dash config equals", []string{"-config=/etc/x.yml"}, []string{"--config=/etc/x.yml"}},
		{"single-dash version", []string{"-version"}, []string{"--version"}},
		{"double-dash untouched", []string{"--config", "/etc/x.yml"}, []string{"--config", "/etc/x.yml"}},
		{"subcommand untouched", []string{"setup", "infra", "nginx"}, []string{"setup", "infra", "nginx"}},
		{"unrelated short flag untouched", []string{"update", "-check"}, []string{"update", "-check"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeLegacyFlags(tt.in)
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("normalizeLegacyFlags(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestRootCmd_RegistersSubcommands(t *testing.T) {
	cmd := newRootCmd()
	for _, name := range []string{"serve", "setup", "update", "version"} {
		sub, _, err := cmd.Find([]string{name})
		if err != nil || sub.Name() != name {
			t.Errorf("expected subcommand %q to be registered, got %v (err %v)", name, sub, err)
		}
	}
}
