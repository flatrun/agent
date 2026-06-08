package api

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/contextkeys"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type authMessage struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

type resizeMessage struct {
	Type string `json:"type"`
	Rows uint16 `json:"rows"`
	Cols uint16 `json:"cols"`
}

func (s *Server) containerExec(c *gin.Context) {
	containerID := c.Param("id")
	if containerID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "container ID required"})
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	if s.authMiddleware.IsAuthEnabled() {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

		_, message, err := conn.ReadMessage()
		if err != nil {
			sendError(conn, "Authentication timeout")
			return
		}

		var auth authMessage
		if err := json.Unmarshal(message, &auth); err != nil || auth.Type != "auth" {
			sendError(conn, "Invalid auth message format")
			return
		}

		actor, err := s.authMiddleware.ActorForTokenString(auth.Token, c.ClientIP())
		if err != nil {
			sendError(conn, "Invalid or expired token")
			return
		}
		c.Set(contextkeys.Actor, actor)

		_ = conn.SetReadDeadline(time.Time{})
	} else {
		c.Set(contextkeys.Actor, &auth.ActorContext{Type: "anonymous", Role: auth.RoleAdmin})
	}

	actor := auth.GetActorFromContext(c)
	if actor == nil || !actor.HasPermission(auth.PermContainersWrite) {
		sendError(conn, "Permission denied: containers:write required")
		return
	}
	if !s.actorCanAccessContainer(c, containerID, auth.AccessLevelWrite) {
		sendError(conn, "No access to this container")
		return
	}
	if deploymentName, err := containerDeploymentName(containerID); err == nil && deploymentName != "" {
		if blocked, reason, err := s.protectedDeploymentActionBlocked(deploymentName, protectedActionTerminal); err != nil {
			sendError(conn, "Failed to check protected mode: "+err.Error())
			return
		} else if blocked {
			sendTerminalError(conn, "protected_mode", reason)
			return
		}
	}
	if s.authMiddleware.IsAuthEnabled() {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"auth_success"}`)); err != nil {
			return
		}
	}

	shell := detectShell(containerID)

	// Create the docker exec command
	cmd := exec.Command("docker", "exec", "-i", "-t", containerID, shell)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	guard := func(command string) (bool, *models.ProtectedCommandRule, error) {
		return s.protectedContainerCommandBlocked(containerID, command)
	}
	streamPTY(conn, cmd, guard)
}

// terminalCommandGuard decides whether a submitted command line may run.
type terminalCommandGuard func(command string) (bool, *models.ProtectedCommandRule, error)

// streamPTY runs cmd under a PTY and pumps bytes between it and the
// websocket: binary frames carry raw terminal bytes, JSON text frames carry
// resizes, and each submitted line is checked against guard before it runs.
func streamPTY(conn *websocket.Conn, cmd *exec.Cmd, guard terminalCommandGuard) {
	ptmx, err := pty.Start(cmd)
	if err != nil {
		sendError(conn, "Failed to start terminal: "+err.Error())
		return
	}
	defer func() {
		ptmx.Close()
		_ = cmd.Process.Kill()
	}()

	// Set initial terminal size
	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: 24, Cols: 80})

	var wg sync.WaitGroup
	done := make(chan struct{})

	// Read from PTY, write to WebSocket
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			select {
			case <-done:
				return
			default:
				n, err := ptmx.Read(buf)
				if err != nil {
					if err != io.EOF {
						log.Printf("PTY read error: %v", err)
					}
					return
				}
				if n > 0 {
					if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
						log.Printf("WebSocket write error: %v", err)
						return
					}
				}
			}
		}
	}()

	// Read from WebSocket, write to PTY
	wg.Add(1)
	go func() {
		defer wg.Done()
		var commandBuffer strings.Builder
		for {
			select {
			case <-done:
				return
			default:
				msgType, message, err := conn.ReadMessage()
				if err != nil {
					if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
						return
					}
					log.Printf("WebSocket read error: %v", err)
					return
				}

				// Check for resize message
				if msgType == websocket.TextMessage {
					var resize resizeMessage
					if err := json.Unmarshal(message, &resize); err == nil && resize.Type == "resize" {
						_ = pty.Setsize(ptmx, &pty.Winsize{Rows: resize.Rows, Cols: resize.Cols})
						continue
					}
				}

				if blocked, err := handleTerminalInput(guard, ptmx, conn, message, &commandBuffer); err != nil {
					log.Printf("PTY write error: %v", err)
					return
				} else if blocked {
					continue
				}

				if _, err := ptmx.Write(message); err != nil {
					log.Printf("PTY write error: %v", err)
					return
				}
			}
		}
	}()

	// Wait for command to finish
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	wg.Wait()
}

func handleTerminalInput(guard terminalCommandGuard, ptmx *os.File, conn *websocket.Conn, message []byte, commandBuffer *strings.Builder) (bool, error) {
	for _, b := range message {
		switch b {
		case '\r', '\n':
			command := strings.TrimSpace(commandBuffer.String())
			commandBuffer.Reset()
			if command == "" {
				continue
			}
			blocked, rule, err := guard(command)
			if err != nil {
				return false, err
			}
			if blocked {
				if _, err := ptmx.Write([]byte{0x15}); err != nil {
					return false, err
				}
				sendTerminalCommandBlocked(conn, command, rule)
				return true, nil
			}
		case 0x03:
			commandBuffer.Reset()
		case 0x7f, 0x08:
			current := commandBuffer.String()
			if len(current) > 0 {
				commandBuffer.Reset()
				commandBuffer.WriteString(current[:len(current)-1])
			}
		default:
			if b >= 0x20 {
				commandBuffer.WriteByte(b)
			}
		}
	}
	return false, nil
}

func detectShell(containerID string) string {
	shells := []string{"/bin/bash", "/bin/sh", "sh"}

	for _, shell := range shells {
		cmd := exec.Command("docker", "exec", containerID, "which", shell)
		cmd.Stdout = nil
		cmd.Stderr = nil
		if err := cmd.Run(); err == nil {
			return shell
		}
	}

	return "sh"
}

func sendError(conn *websocket.Conn, msg string) {
	_ = conn.WriteMessage(websocket.TextMessage, []byte("\r\n\x1b[31mError: "+msg+"\x1b[0m\r\n"))
}

func sendTerminalError(conn *websocket.Conn, code, msg string) {
	payload, err := json.Marshal(gin.H{
		"type":    "error",
		"code":    code,
		"message": msg,
	})
	if err != nil {
		sendError(conn, msg)
		return
	}
	_ = conn.WriteMessage(websocket.TextMessage, payload)
}

func sendTerminalCommandBlocked(conn *websocket.Conn, command string, rule *models.ProtectedCommandRule) {
	ruleLabel := ""
	if rule != nil {
		ruleLabel = rule.Name
		if ruleLabel == "" {
			ruleLabel = rule.ID
		}
	}
	msg := "\r\n\x1b[31mCommand blocked: " + command + "\x1b[0m"
	if ruleLabel != "" {
		msg += "\r\n\x1b[90mRule: " + ruleLabel + "\x1b[0m"
	}
	msg += "\r\n"
	_ = conn.WriteMessage(websocket.TextMessage, []byte(msg))
}

func (s *Server) containerExecHTTP(c *gin.Context) {
	containerID := c.Param("id")
	if !s.requireContainerAccess(c, containerID, auth.AccessLevelWrite) {
		return
	}

	var req struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Command == "" {
		req.Command = "sh"
		req.Args = []string{"-c", "echo 'Shell ready'"}
	}

	commandLine := strings.Join(append([]string{req.Command}, req.Args...), " ")
	if deploymentName, err := containerDeploymentName(containerID); err == nil && deploymentName != "" {
		if blocked, reason, err := s.protectedDeploymentActionBlocked(deploymentName, protectedActionExec); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check protected mode: " + err.Error()})
			return
		} else if blocked {
			c.JSON(http.StatusLocked, gin.H{
				"error":  reason,
				"action": protectedActionExec,
				"reason": reason,
			})
			return
		}
	}
	blocked, rule, err := s.protectedContainerCommandBlocked(containerID, commandLine)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check protected command rules: " + err.Error()})
		return
	}
	if blocked {
		ruleName := rule.Name
		if ruleName == "" {
			ruleName = rule.ID
		}
		c.JSON(http.StatusLocked, gin.H{
			"error":   "Command blocked by deployment protected mode",
			"command": commandLine,
			"rule":    ruleName,
			"match":   rule.Match,
			"pattern": rule.Pattern,
		})
		return
	}

	args := append([]string{"exec", containerID, req.Command}, req.Args...)
	cmd := exec.Command("docker", args...)
	cmd.Env = os.Environ()

	output, err := cmd.CombinedOutput()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":  err.Error(),
			"output": string(output),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"output": string(output),
	})
}
