package api

import (
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
)

func TestRunSystemTerminalCommandAllowsInspectionCommands(t *testing.T) {
	session := &systemTerminalSession{cwd: t.TempDir()}
	server := &Server{config: &config.Config{}}

	output, err := server.runSystemTerminalCommand(session, "pwd")
	if err != nil {
		t.Fatalf("pwd failed: %v", err)
	}
	if !strings.Contains(output, session.cwd) {
		t.Fatalf("pwd output %q does not contain cwd %q", output, session.cwd)
	}
}

func TestRunSystemTerminalCommandAllowsNonAllowlistedCommands(t *testing.T) {
	session := &systemTerminalSession{cwd: t.TempDir()}
	server := &Server{config: &config.Config{}}

	output, err := server.runSystemTerminalCommand(session, "printf system-terminal")
	if err != nil {
		t.Fatalf("printf failed: %v", err)
	}
	if !strings.Contains(output, "system-terminal") {
		t.Fatalf("output = %q, want output containing system-terminal", output)
	}
}

func TestRunSystemTerminalCommandChangesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	session := &systemTerminalSession{cwd: "/"}
	server := &Server{config: &config.Config{}}

	if _, err := server.runSystemTerminalCommand(session, "cd "+tmpDir); err != nil {
		t.Fatalf("cd failed: %v", err)
	}
	if session.cwd != tmpDir {
		t.Fatalf("cwd = %q, want %q", session.cwd, tmpDir)
	}
}

func TestRunSystemTerminalCommandAppliesGlobalProtectedRules(t *testing.T) {
	session := &systemTerminalSession{cwd: t.TempDir()}
	server := &Server{config: &config.Config{
		SystemTerminal: config.SystemTerminalConfig{
			ProtectedMode: models.ProtectedModeConfig{
				Enabled: true,
				BlockedCommandRules: []models.ProtectedCommandRule{{
					ID:      "remove",
					Match:   "contains",
					Pattern: "rm -rf",
				}},
			},
		},
	}}

	_, err := server.runSystemTerminalCommand(session, "rm -rf app")
	if err == nil {
		t.Fatal("expected command to be blocked")
	}
	if !strings.Contains(err.Error(), "Command blocked") {
		t.Fatalf("unexpected error: %v", err)
	}
}
