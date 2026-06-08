package api

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// systemTerminalInteractive runs the host shell under a real PTY over a
// websocket, using the same stream protocol as the container exec terminal.
// The JSON command endpoint stays as the API surface for programmatic use.
func (s *Server) systemTerminalInteractive(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	if !s.authenticateSystemTerminal(c, conn) {
		return
	}
	if s.config != nil && s.config.SystemTerminal.ProtectedMode.Enabled && s.config.SystemTerminal.ProtectedMode.DisableTerminal {
		sendTerminalError(conn, "protected_mode", "System terminal access is disabled by global protected mode settings")
		return
	}
	if s.authMiddleware != nil && s.authMiddleware.IsAuthEnabled() {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"auth_success"}`)); err != nil {
			return
		}
	}

	cmd := exec.Command(hostShell(), "-l")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	if s.config != nil && s.config.DeploymentsPath != "" {
		if abs, err := filepath.Abs(s.config.DeploymentsPath); err == nil {
			cmd.Dir = abs
		}
	}

	guard := func(command string) (bool, *models.ProtectedCommandRule, error) {
		if s.config == nil {
			return false, nil, nil
		}
		return protectedCommandBlocked(&s.config.SystemTerminal.ProtectedMode, command)
	}
	streamPTY(conn, cmd, guard)
}

func hostShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	for _, shell := range []string{"/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(shell); err == nil {
			return shell
		}
	}
	return "sh"
}
