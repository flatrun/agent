package api

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
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

	// First-message authentication
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

		if !s.authMiddleware.ValidateTokenString(auth.Token) {
			sendError(conn, "Invalid or expired token")
			return
		}

		_ = conn.SetReadDeadline(time.Time{})

		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"auth_success"}`)); err != nil {
			return
		}
	}

	shell := detectShell(containerID)

	// Create the docker exec command
	cmd := exec.Command("docker", "exec", "-i", "-t", containerID, shell)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	// Start with PTY
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

				// Write input to PTY
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

func (s *Server) containerExecHTTP(c *gin.Context) {
	containerID := c.Param("id")

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
