package audit

import (
	"bytes"
	"io"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/flatrun/agent/internal/contextkeys"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Middleware struct {
	manager *Manager
}

func NewMiddleware(manager *Manager) *Middleware {
	return &Middleware{manager: manager}
}

func (m *Middleware) Capture() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !m.manager.IsEnabled() {
			c.Next()
			return
		}

		if !m.manager.ShouldCapturePath(c.Request.URL.Path) {
			c.Next()
			return
		}

		startTime := time.Now()
		requestID := uuid.New().String()
		c.Set(contextkeys.RequestID, requestID)

		var requestBody string
		if m.manager.ShouldCaptureBody() && c.Request.Body != nil {
			bodyBytes, _ := io.ReadAll(c.Request.Body)
			c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
			requestBody = string(bodyBytes)
			if len(requestBody) > 10000 {
				requestBody = requestBody[:10000] + "...[truncated]"
			}
		}

		c.Next()

		event := &AuditEvent{
			EventID:        requestID,
			Timestamp:      startTime,
			ActorType:      m.getActorType(c),
			ActorID:        m.getStringContext(c, contextkeys.ActorID),
			ActorName:      m.getStringContext(c, contextkeys.ActorName),
			APIKeyPrefix:   m.getStringContext(c, contextkeys.APIKeyPrefix),
			Action:         m.determineAction(c),
			Method:         c.Request.Method,
			Path:           c.Request.URL.Path,
			ResourceType:   m.extractResourceType(c),
			ResourceID:     m.extractResourceID(c),
			ClientIP:       c.ClientIP(),
			UserAgent:      c.Request.UserAgent(),
			RequestID:      requestID,
			RequestBody:    requestBody,
			ResponseStatus: c.Writer.Status(),
			ResponseTimeMs: time.Since(startTime).Milliseconds(),
			Success:        c.Writer.Status() < 400,
			ErrorMessage:   c.Errors.String(),
		}

		go func() {
			if err := m.manager.LogEvent(event); err != nil {
				log.Printf("Failed to log audit event: %v", err)
			}
		}()
	}
}

func (m *Middleware) getActorType(c *gin.Context) ActorType {
	if val, exists := c.Get(contextkeys.ActorType); exists {
		if actorType, ok := val.(ActorType); ok {
			return actorType
		}
		if str, ok := val.(string); ok {
			return ActorType(str)
		}
	}
	return ActorTypeAnonymous
}

func (m *Middleware) getStringContext(c *gin.Context, key string) string {
	if val, exists := c.Get(key); exists {
		if str, ok := val.(string); ok {
			return str
		}
	}
	return ""
}

func (m *Middleware) determineAction(c *gin.Context) string {
	path := c.Request.URL.Path
	method := c.Request.Method

	path = strings.TrimPrefix(path, "/api/")
	path = strings.TrimPrefix(path, "/api/v1/")

	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return method + "_unknown"
	}

	resource := parts[0]
	if len(parts) > 1 && !isID(parts[1]) {
		resource = parts[0] + "_" + parts[1]
	}

	var action string
	switch method {
	case "GET":
		if len(parts) > 1 && isID(parts[len(parts)-1]) {
			action = "read"
		} else {
			action = "list"
		}
	case "POST":
		action = "create"
	case "PUT", "PATCH":
		action = "update"
	case "DELETE":
		action = "delete"
	default:
		action = strings.ToLower(method)
	}

	return resource + "." + action
}

func (m *Middleware) extractResourceType(c *gin.Context) string {
	path := c.Request.URL.Path
	path = strings.TrimPrefix(path, "/api/")
	path = strings.TrimPrefix(path, "/api/v1/")

	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return ""
	}

	return parts[0]
}

func (m *Middleware) extractResourceID(c *gin.Context) string {
	if id := c.Param("name"); id != "" {
		return id
	}
	if id := c.Param("id"); id != "" {
		return id
	}

	path := c.Request.URL.Path
	parts := strings.Split(path, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if isID(parts[i]) {
			return parts[i]
		}
	}

	return ""
}

var uuidRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var numericRegex = regexp.MustCompile(`^\d+$`)

func isID(s string) bool {
	if s == "" {
		return false
	}
	if uuidRegex.MatchString(s) {
		return true
	}
	if numericRegex.MatchString(s) {
		return true
	}
	if len(s) >= 8 && len(s) <= 64 && !strings.Contains(s, " ") {
		alphaNum := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
		if alphaNum.MatchString(s) && strings.ContainsAny(s, "0123456789") {
			return true
		}
	}
	return false
}
