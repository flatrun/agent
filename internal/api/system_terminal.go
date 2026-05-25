package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/contextkeys"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

type systemTerminalMessage struct {
	Type    string `json:"type"`
	Token   string `json:"token,omitempty"`
	Command string `json:"command,omitempty"`
}

type systemTerminalSession struct {
	cwd string
}

func (s *Server) systemTerminal(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	if !s.authenticateSystemTerminal(c, conn) {
		return
	}
	if s.config != nil && s.config.SystemTerminal.ProtectedMode.Enabled && s.config.SystemTerminal.ProtectedMode.DisableTerminal {
		sendSystemTerminalError(conn, "System terminal access is disabled by global protected mode settings")
		return
	}

	cwd := "/"
	if s.config != nil && s.config.DeploymentsPath != "" {
		if abs, err := filepath.Abs(s.config.DeploymentsPath); err == nil {
			cwd = abs
		}
	}
	session := &systemTerminalSession{cwd: cwd}

	_ = conn.WriteJSON(gin.H{"type": "auth_success"})
	sendSystemTerminalOutput(conn, fmt.Sprintf("Connected to system terminal\nWorking directory: %s\n%s", session.cwd, systemPrompt(session.cwd)))

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var req systemTerminalMessage
		if err := json.Unmarshal(message, &req); err != nil {
			sendSystemTerminalError(conn, "Invalid terminal message")
			continue
		}
		if req.Type != "command" {
			continue
		}

		output, err := s.runSystemTerminalCommand(session, req.Command)
		if err != nil {
			sendSystemTerminalError(conn, err.Error()+"\n"+systemPrompt(session.cwd))
			continue
		}
		sendSystemTerminalOutput(conn, output+systemPrompt(session.cwd))
	}
}

func (s *Server) authenticateSystemTerminal(c *gin.Context, conn *websocket.Conn) bool {
	if s.authMiddleware == nil || !s.authMiddleware.IsAuthEnabled() {
		c.Set(contextkeys.Actor, &auth.ActorContext{Type: "anonymous", Role: auth.RoleAdmin})
		return true
	}

	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, message, err := conn.ReadMessage()
	if err != nil {
		sendSystemTerminalError(conn, "Authentication timeout")
		return false
	}

	var authMsg systemTerminalMessage
	if err := json.Unmarshal(message, &authMsg); err != nil || authMsg.Type != "auth" {
		sendSystemTerminalError(conn, "Invalid auth message format")
		return false
	}

	actor, err := s.authMiddleware.ActorForTokenString(authMsg.Token, c.ClientIP())
	if err != nil {
		sendSystemTerminalError(conn, "Invalid or expired token")
		return false
	}
	if !actor.HasPermission(auth.PermSystemWrite) {
		sendSystemTerminalError(conn, "Permission denied: system:write required")
		return false
	}

	c.Set(contextkeys.Actor, actor)
	_ = conn.SetReadDeadline(time.Time{})
	return true
}

func (s *Server) runSystemTerminalCommand(session *systemTerminalSession, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}

	if s != nil && s.config != nil {
		blocked, rule, err := protectedCommandBlocked(&s.config.SystemTerminal.ProtectedMode, raw)
		if err != nil {
			return "", err
		}
		if blocked {
			return "", errors.New(protectedCommandBlockMessage(raw, rule))
		}
	}

	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return "", nil
	}

	command := parts[0]
	args := parts[1:]
	if command == "ll" {
		command = "ls"
		args = append([]string{"-la"}, args...)
		raw = strings.Join(append([]string{command}, args...), " ")
	}
	if command == "la" {
		command = "ls"
		args = append([]string{"-A"}, args...)
		raw = strings.Join(append([]string{command}, args...), " ")
	}

	if command == "cd" {
		target := session.cwd
		if len(args) > 0 {
			target = resolveSystemTerminalPath(session.cwd, args[0])
		}
		info, err := os.Stat(target)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("not a directory: %s", target)
		}
		session.cwd = target
		return "", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-lc", raw)
	cmd.Dir = session.cwd
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(output), fmt.Errorf("command timed out")
	}
	if err != nil {
		msg := strings.TrimSpace(string(output))
		if msg == "" {
			msg = err.Error()
		}
		return string(output), errors.New(msg)
	}
	return string(output), nil
}

func resolveSystemTerminalPath(cwd, target string) string {
	if target == "" || target == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if filepath.IsAbs(target) {
		return filepath.Clean(target)
	}
	return filepath.Clean(filepath.Join(cwd, target))
}

func systemPrompt(cwd string) string {
	return fmt.Sprintf("\r\n%s $ ", cwd)
}

func sendSystemTerminalOutput(conn *websocket.Conn, output string) {
	_ = conn.WriteJSON(gin.H{"type": "output", "data": output})
}

func sendSystemTerminalError(conn *websocket.Conn, msg string) {
	_ = conn.WriteJSON(gin.H{"type": "error", "message": msg})
}
