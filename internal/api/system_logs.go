package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/contextkeys"
	"github.com/flatrun/agent/internal/infra"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// systemLogSource is one place the host itself writes logs, as opposed to a deployment: the
// proxy's access and error logs, and whatever shared infrastructure is running.
type systemLogSource struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Service string `json:"service"`
	Stream  string `json:"stream"`
	// ByDeployment says whether lines carry which deployment they belong to, which only the
	// proxy's access log does.
	ByDeployment bool `json:"by_deployment"`
}

func systemLogSourcesFor(services []models.InfraService) []systemLogSource {
	var sources []systemLogSource
	for _, svc := range services {
		switch svc.Type {
		case models.InfraTypeNginx:
			sources = append(sources,
				systemLogSource{ID: "nginx-access", Name: "nginx access", Service: svc.Name, Stream: infra.LogStreamStdout, ByDeployment: true},
				systemLogSource{ID: "nginx-error", Name: "nginx error", Service: svc.Name, Stream: infra.LogStreamStderr},
			)
		default:
			sources = append(sources, systemLogSource{
				ID:      svc.Name,
				Name:    svc.Name,
				Service: svc.Name,
				Stream:  infra.LogStreamAll,
			})
		}
	}
	return sources
}

func (s *Server) systemLogSources() ([]systemLogSource, error) {
	services, err := s.infraManager.ListServices()
	if err != nil {
		return nil, err
	}
	return systemLogSourcesFor(services), nil
}

func (s *Server) resolveSystemLogSource(id string) (systemLogSource, bool) {
	sources, err := s.systemLogSources()
	if err != nil {
		return systemLogSource{}, false
	}
	if id == "" && len(sources) > 0 {
		return sources[0], true
	}
	for _, src := range sources {
		if src.ID == id {
			return src, true
		}
	}
	return systemLogSource{}, false
}

func (s *Server) listSystemLogSources(c *gin.Context) {
	sources, err := s.systemLogSources()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if sources == nil {
		sources = []systemLogSource{}
	}
	c.JSON(http.StatusOK, gin.H{"sources": sources})
}

// deploymentHostMatcher keeps only the access lines belonging to one deployment, by matching
// the host the request asked for against the domains that deployment serves. The host is a
// field of its own in the access log, so this never matches a domain that merely appears in a
// referer or a user agent.
func (s *Server) deploymentHostMatcher(name string) (func(string) bool, error) {
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		return nil, err
	}

	hosts := map[string]struct{}{}
	if deployment.Metadata != nil {
		for _, d := range deployment.Metadata.GetDomains() {
			if d.Domain != "" {
				hosts[strings.ToLower(d.Domain)] = struct{}{}
			}
		}
	}
	if len(hosts) == 0 {
		// Nothing is served for this deployment, so no access line can belong to it.
		return func(string) bool { return false }, nil
	}

	return func(line string) bool {
		// Lines arrive with docker's timestamp ahead of nginx's own fields, so the host is
		// one of the first two fields depending on whether that prefix is present.
		fields := strings.Fields(line)
		for i := 0; i < len(fields) && i < 2; i++ {
			host := strings.ToLower(strings.TrimSuffix(fields[i], ":443"))
			host = strings.TrimSuffix(host, ":80")
			if _, ok := hosts[host]; ok {
				return true
			}
		}
		return false
	}, nil
}

// systemLogLineFilter combines every reason to drop a line into one test.
func (s *Server) systemLogLineFilter(c *gin.Context, src systemLogSource) (func(string) bool, bool) {
	filter := strings.ToLower(c.Query("filter"))
	deployment := c.Query("deployment")

	var matchesDeployment func(string) bool
	if deployment != "" {
		if !src.ByDeployment {
			c.JSON(http.StatusBadRequest, gin.H{"error": "this log source does not say which deployment a line belongs to"})
			return nil, false
		}
		if !s.requireDeploymentAccess(c, deployment, auth.AccessLevelRead) {
			return nil, false
		}
		matcher, err := s.deploymentHostMatcher(deployment)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
			return nil, false
		}
		matchesDeployment = matcher
	}

	return func(line string) bool {
		if filter != "" && !strings.Contains(strings.ToLower(line), filter) {
			return false
		}
		if matchesDeployment != nil && !matchesDeployment(line) {
			return false
		}
		return true
	}, true
}

func (s *Server) getSystemLogs(c *gin.Context) {
	if s.infraManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Infrastructure not available"})
		return
	}

	src, ok := s.resolveSystemLogSource(c.Query("source"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown log source"})
		return
	}

	tail, err := strconv.Atoi(c.DefaultQuery("tail", "100"))
	if err != nil {
		tail = 100
	}

	keep, ok := s.systemLogLineFilter(c, src)
	if !ok {
		return
	}

	logs, err := s.infraManager.ServiceLogs(src.Service, tail, src.Stream)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	kept := make([]string, 0)
	for _, line := range strings.Split(strings.TrimRight(logs, "\n"), "\n") {
		if line == "" || !keep(line) {
			continue
		}
		kept = append(kept, line)
	}
	text := strings.Join(kept, "\n")

	records := parseLogRecords(text)
	for i := range records {
		if records[i].Service == "" {
			records[i].Service = src.Name
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"source":  src.ID,
		"logs":    text,
		"records": records,
	})
}

// streamSystemLogs follows a system log source over a websocket until the viewer goes away,
// the same way a deployment's logs are followed.
func (s *Server) streamSystemLogs(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("system log stream: websocket upgrade failed: %v", err)
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
	if actor == nil || !actor.HasPermission(auth.PermInfrastructureRead) {
		sendError(conn, "Permission denied: infrastructure:read required")
		return
	}

	if s.authMiddleware.IsAuthEnabled() {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"auth_success"}`)); err != nil {
			return
		}
	}

	if s.infraManager == nil {
		sendError(conn, "Infrastructure not available")
		return
	}

	src, ok := s.resolveSystemLogSource(c.Query("source"))
	if !ok {
		sendError(conn, "Unknown log source")
		return
	}

	tail := 100
	if v := c.Query("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			tail = n
		}
	}

	// A deployment's own domains decide which access lines it may see, so the same check the
	// snapshot makes applies here; the socket carries no body to report it in.
	deployment := c.Query("deployment")
	var matchesDeployment func(string) bool
	if deployment != "" {
		if !src.ByDeployment {
			sendError(conn, "This log source does not say which deployment a line belongs to")
			return
		}
		if actor.Role != auth.RoleAdmin && !actor.CanAccessDeployment(deployment, auth.AccessLevelRead) {
			sendError(conn, "No access to this deployment")
			return
		}
		matcher, err := s.deploymentHostMatcher(deployment)
		if err != nil {
			sendError(conn, "Deployment not found")
			return
		}
		matchesDeployment = matcher
	}

	filter := strings.ToLower(c.Query("filter"))

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

	sink := func(line string) {
		if filter != "" && !strings.Contains(strings.ToLower(line), filter) {
			return
		}
		if matchesDeployment != nil && !matchesDeployment(line) {
			return
		}
		record := parseLogRecord(line)
		if record.Service == "" {
			record.Service = src.Name
		}
		payload, err := json.Marshal(logLine{Type: "log", Line: line, Record: record})
		if err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, payload)
	}

	if err := s.infraManager.StreamServiceLogs(ctx, src.Service, tail, src.Stream, sink); err != nil && ctx.Err() == nil {
		sendError(conn, err.Error())
		return
	}

	_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"end"}`))
}
