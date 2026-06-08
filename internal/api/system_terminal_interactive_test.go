package api

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func dialSystemTerminalInteractive(t *testing.T, cfg *config.Config) *websocket.Conn {
	t.Helper()
	gin.SetMode(gin.TestMode)

	server := &Server{config: cfg}
	router := gin.New()
	router.GET("/system/terminal/interactive", server.systemTerminalInteractive)

	httpServer := newSkippableHTTPServer(t, router)
	t.Cleanup(httpServer.Close)

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/system/terminal/interactive"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func requirePTY(t *testing.T) {
	t.Helper()
	cmd := exec.Command("sh", "-c", "true")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Skipf("PTY not available in this environment: %v", err)
	}
	ptmx.Close()
	_ = cmd.Wait()
}

// readUntil collects frames until the predicate matches or the deadline hits.
func readUntil(t *testing.T, conn *websocket.Conn, want func(string) bool) string {
	t.Helper()
	var all strings.Builder
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, message, err := conn.ReadMessage()
		if err != nil {
			continue
		}
		all.Write(message)
		if want(all.String()) {
			return all.String()
		}
	}
	return all.String()
}

func TestSystemTerminalInteractiveDisabledByProtectedMode(t *testing.T) {
	cfg := &config.Config{
		SystemTerminal: config.SystemTerminalConfig{
			ProtectedMode: models.ProtectedModeConfig{
				Enabled:         true,
				DisableTerminal: true,
			},
		},
	}
	conn := dialSystemTerminalInteractive(t, cfg)

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, message, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read websocket response: %v", err)
	}

	var payload struct {
		Type string `json:"type"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal(message, &payload); err != nil {
		t.Fatalf("expected JSON error payload, got %q", message)
	}
	if payload.Type != "error" || payload.Code != "protected_mode" {
		t.Fatalf("expected protected_mode error, got %q", message)
	}
}

func TestSystemTerminalInteractiveRunsShellUnderPTY(t *testing.T) {
	requirePTY(t)

	conn := dialSystemTerminalInteractive(t, &config.Config{DeploymentsPath: t.TempDir()})

	// tty only succeeds with a real terminal attached; piped execution
	// prints "not a tty" instead of a device path.
	if err := conn.WriteMessage(websocket.TextMessage, []byte("tty && echo flatrun-pty-ok\r")); err != nil {
		t.Fatalf("failed to send command: %v", err)
	}

	output := readUntil(t, conn, func(s string) bool { return strings.Contains(s, "flatrun-pty-ok") })
	if !strings.Contains(output, "flatrun-pty-ok") {
		t.Fatalf("shell did not run under a tty, output:\n%s", output)
	}
	if strings.Contains(output, "not a tty") {
		t.Fatalf("shell reports no tty, output:\n%s", output)
	}
}

func TestSystemTerminalInteractiveBlocksProtectedCommands(t *testing.T) {
	requirePTY(t)

	cfg := &config.Config{
		DeploymentsPath: t.TempDir(),
		SystemTerminal: config.SystemTerminalConfig{
			ProtectedMode: models.ProtectedModeConfig{
				Enabled: true,
				BlockedCommandRules: []models.ProtectedCommandRule{
					{ID: "no-secrets", Name: "No secret reads", Match: "contains", Pattern: "cat /etc/shadow"},
				},
			},
		},
	}
	conn := dialSystemTerminalInteractive(t, cfg)

	if err := conn.WriteMessage(websocket.TextMessage, []byte("cat /etc/shadow\r")); err != nil {
		t.Fatalf("failed to send command: %v", err)
	}
	output := readUntil(t, conn, func(s string) bool { return strings.Contains(s, "Command blocked") })
	if !strings.Contains(output, "Command blocked") {
		t.Fatalf("expected the command to be blocked, output:\n%s", output)
	}

	// A harmless command must still run after the block.
	if err := conn.WriteMessage(websocket.TextMessage, []byte("echo flatrun-still-alive\r")); err != nil {
		t.Fatalf("failed to send follow-up command: %v", err)
	}
	output = readUntil(t, conn, func(s string) bool { return strings.Contains(s, "flatrun-still-alive") })
	if !strings.Contains(output, "flatrun-still-alive") {
		t.Fatalf("terminal did not survive the blocked command, output:\n%s", output)
	}
}
