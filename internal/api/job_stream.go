package api

import (
	"encoding/json"
	"log"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/contextkeys"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// streamDeploymentJob streams an action job's output over a websocket. It
// mirrors the terminal endpoints: the connection authenticates with a
// first-message token, then the buffered output is replayed before live lines
// are pushed, and a final result frame is sent when the job finishes.
func (s *Server) streamDeploymentJob(c *gin.Context) {
	name := c.Param("name")
	jobID := c.Param("jobId")

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
			sendJobError(conn, "Authentication timeout")
			return
		}

		var am authMessage
		if err := json.Unmarshal(message, &am); err != nil || am.Type != "auth" {
			sendJobError(conn, "Invalid auth message format")
			return
		}

		actor, err := s.authMiddleware.ActorForTokenString(am.Token, c.ClientIP())
		if err != nil {
			sendJobError(conn, "Invalid or expired token")
			return
		}
		c.Set(contextkeys.Actor, actor)

		_ = conn.SetReadDeadline(time.Time{})
	} else {
		c.Set(contextkeys.Actor, &auth.ActorContext{Type: "anonymous", Role: auth.RoleAdmin})
	}

	actor := auth.GetActorFromContext(c)
	if actor == nil || !actor.HasPermission(auth.PermDeploymentsRead) {
		sendJobError(conn, "Permission denied: deployments:read required")
		return
	}
	if !actor.CanAccessDeployment(name, auth.AccessLevelRead) {
		sendJobError(conn, "No access to this deployment")
		return
	}
	if s.authMiddleware.IsAuthEnabled() {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"auth_success"}`)); err != nil {
			return
		}
	}

	job := s.jobs.get(jobID)
	if job == nil || job.Deployment() != name {
		sendJobError(conn, "Job not found")
		return
	}

	buffered, lines, cancel := job.subscribe()
	defer cancel()

	for _, line := range buffered {
		if err := writeJobLine(conn, line); err != nil {
			return
		}
	}
	for line := range lines {
		if err := writeJobLine(conn, line); err != nil {
			return
		}
	}

	snap := job.snapshot()
	_ = conn.WriteMessage(websocket.TextMessage, mustJSON(gin.H{
		"type":   "result",
		"status": string(snap.Status),
		"error":  snap.Error,
	}))
}

func sendJobError(conn *websocket.Conn, msg string) {
	_ = conn.WriteMessage(websocket.TextMessage, mustJSON(gin.H{
		"type":    "error",
		"message": msg,
	}))
}

func writeJobLine(conn *websocket.Conn, line string) error {
	return conn.WriteMessage(websocket.TextMessage, mustJSON(gin.H{
		"type": "line",
		"data": line,
	}))
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"type":"error","message":"failed to encode message"}`)
	}
	return b
}
