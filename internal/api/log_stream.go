package api

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/contextkeys"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// logLine is one line of a followed log, as the viewer receives it. Line is the
// raw compose output; Record is that same line broken into structured parts so a
// viewer can render it as an expandable row instead of flat text.
type logLine struct {
	Type   string    `json:"type"`
	Line   string    `json:"line"`
	Record logRecord `json:"record"`
}

// streamDeploymentLogs follows a deployment's logs over a websocket until the viewer goes
// away.
//
// Reading logs as a tail means a viewer only ever knows what was true when it asked, so
// watching a container start is a matter of reloading and hoping. Following hands over each
// line as the container writes it.
func (s *Server) streamDeploymentLogs(c *gin.Context) {
	name := c.Param("name")

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("log stream: websocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	// The browser cannot set headers on a websocket, so the token arrives as the first
	// message, the same way the terminal does it.
	if s.authMiddleware.IsAuthEnabled() {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

		_, message, err := conn.ReadMessage()
		if err != nil {
			sendError(conn, "Authentication timeout")
			return
		}

		var incoming authMessage
		if err := json.Unmarshal(message, &incoming); err != nil || incoming.Type != "auth" {
			sendError(conn, "Invalid auth message format")
			return
		}

		actor, err := s.authMiddleware.ActorForTokenString(incoming.Token, c.ClientIP())
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
	if actor == nil || !actor.HasPermission(auth.PermDeploymentsRead) {
		sendError(conn, "Permission denied: deployments:read required")
		return
	}
	// Logs carry whatever the application prints, so reading them needs the same access as
	// reading the deployment itself.
	if !actor.CanAccessDeployment(name, auth.AccessLevelRead) {
		sendError(conn, "No access to this deployment")
		return
	}

	if s.authMiddleware.IsAuthEnabled() {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"auth_success"}`)); err != nil {
			return
		}
	}

	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		sendError(conn, "Deployment not found")
		return
	}

	tail := 100
	if v := c.Query("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			tail = n
		}
	}
	// Filtering here rather than in the browser means a noisy container does not have to
	// push everything it writes down the socket for the viewer to throw away.
	filter := strings.ToLower(c.Query("filter"))

	// The stream ends when the viewer disconnects, which is what stops the compose process
	// rather than leaving it attached for the life of the agent.
	ctx := c.Request.Context()

	// Nothing is expected from the viewer, but reading is how a closed socket is noticed.
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				conn.Close()
				return
			}
		}
	}()

	err = s.manager.StreamDeploymentLogs(ctx, name, deployment.Path, tail, func(line string) {
		if filter != "" && !strings.Contains(strings.ToLower(line), filter) {
			return
		}
		payload, err := json.Marshal(logLine{Type: "log", Line: line, Record: parseLogRecord(line)})
		if err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, payload)
	})
	if err != nil && ctx.Err() == nil {
		sendError(conn, err.Error())
		return
	}

	_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"end"}`))
}

// filterLogLines keeps only the lines containing needle, so a client asking for a snapshot of
// a noisy container is not sent everything it wrote in order to throw most of it away.
func filterLogLines(logs, filter string) string {
	if filter == "" {
		return logs
	}
	needle := strings.ToLower(filter)

	var kept []string
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(strings.ToLower(line), needle) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}
