package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/flatrun/agent/internal/security"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

// ingestSecurityEvent handles real-time security event ingestion from nginx Lua
func (s *Server) ingestSecurityEvent(c *gin.Context) {
	if s.securityManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Security module not enabled"})
		return
	}

	var event security.IngestEvent
	if err := c.ShouldBindJSON(&event); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	secEvent, err := s.securityManager.IngestEvent(&event)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if secEvent == nil {
		c.JSON(http.StatusOK, gin.H{"processed": false, "reason": "Event not security-relevant or IP blocked"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"processed": true, "event": secEvent})
}

// getSecurityStats returns security statistics
func (s *Server) getSecurityStats(c *gin.Context) {
	if s.securityManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Security module not enabled"})
		return
	}

	stats, err := s.securityManager.GetStats()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"stats": stats})
}

// listSecurityEvents returns a paginated list of security events
func (s *Server) listSecurityEvents(c *gin.Context) {
	if s.securityManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Security module not enabled"})
		return
	}

	filter := &security.EventFilter{
		EventType:      c.Query("event_type"),
		Severity:       c.Query("severity"),
		SourceIP:       c.Query("source_ip"),
		DeploymentName: c.Query("deployment"),
	}

	if limit := c.Query("limit"); limit != "" {
		if l, err := strconv.Atoi(limit); err == nil {
			filter.Limit = l
		}
	} else {
		filter.Limit = 50
	}

	if offset := c.Query("offset"); offset != "" {
		if o, err := strconv.Atoi(offset); err == nil {
			filter.Offset = o
		}
	}

	if startTime := c.Query("start_time"); startTime != "" {
		if t, err := time.Parse(time.RFC3339, startTime); err == nil {
			filter.StartTime = t
		}
	}

	if endTime := c.Query("end_time"); endTime != "" {
		if t, err := time.Parse(time.RFC3339, endTime); err == nil {
			filter.EndTime = t
		}
	}

	events, total, err := s.securityManager.GetEvents(filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"events": events,
		"total":  total,
		"limit":  filter.Limit,
		"offset": filter.Offset,
	})
}

// getSecurityEvent returns a single security event by ID
func (s *Server) getSecurityEvent(c *gin.Context) {
	if s.securityManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Security module not enabled"})
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid event ID"})
		return
	}

	event, err := s.securityManager.GetEventByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Event not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"event": event})
}

// cleanupSecurityEvents removes old security events
func (s *Server) cleanupSecurityEvents(c *gin.Context) {
	if s.securityManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Security module not enabled"})
		return
	}

	var req struct {
		Days int `json:"days"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		req.Days = s.config.Security.RetentionDays
	}
	if req.Days <= 0 {
		req.Days = 30
	}

	eventsDeleted, blocksDeleted, err := s.securityManager.Cleanup(req.Days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"events_deleted": eventsDeleted,
		"blocks_deleted": blocksDeleted,
	})
}

// listBlockedIPs returns all blocked IPs
func (s *Server) listBlockedIPs(c *gin.Context) {
	if s.securityManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Security module not enabled"})
		return
	}

	ips, err := s.securityManager.GetBlockedIPs()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"blocked_ips": ips})
}

// blockIP blocks an IP address
func (s *Server) blockIP(c *gin.Context) {
	if s.securityManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Security module not enabled"})
		return
	}

	var req struct {
		IP       string `json:"ip" binding:"required"`
		Reason   string `json:"reason"`
		Duration int    `json:"duration"` // seconds, 0 = permanent
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	id, err := s.securityManager.BlockIP(req.IP, req.Reason, req.Duration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": id, "message": "IP blocked successfully"})
}

// unblockIP unblocks an IP address
func (s *Server) unblockIP(c *gin.Context) {
	if s.securityManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Security module not enabled"})
		return
	}

	ip := c.Param("ip")
	if ip == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "IP address required"})
		return
	}

	if err := s.securityManager.UnblockIP(ip); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "IP unblocked successfully"})
}

// getEventsByIP returns all security events for a specific IP
func (s *Server) getEventsByIP(c *gin.Context) {
	if s.securityManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Security module not enabled"})
		return
	}

	ip := c.Param("ip")
	if ip == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "IP address required"})
		return
	}

	events, err := s.securityManager.GetEventsByIP(ip)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"events": events, "ip": ip})
}

// listProtectedRoutes returns all protected routes
func (s *Server) listProtectedRoutes(c *gin.Context) {
	if s.securityManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Security module not enabled"})
		return
	}

	routes, err := s.securityManager.GetProtectedRoutes()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"protected_routes": routes})
}

// addProtectedRoute adds a new protected route
func (s *Server) addProtectedRoute(c *gin.Context) {
	if s.securityManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Security module not enabled"})
		return
	}

	var route security.ProtectedRoute
	if err := c.ShouldBindJSON(&route); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if route.PathPattern == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path_pattern is required"})
		return
	}
	if route.RateLimit <= 0 {
		route.RateLimit = 10
	}
	if route.BlockDuration <= 0 {
		route.BlockDuration = 3600
	}
	route.Enabled = true

	id, err := s.securityManager.AddProtectedRoute(&route)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	route.ID = id
	c.JSON(http.StatusCreated, gin.H{"route": route})
}

// updateProtectedRoute updates an existing protected route
func (s *Server) updateProtectedRoute(c *gin.Context) {
	if s.securityManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Security module not enabled"})
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid route ID"})
		return
	}

	var route security.ProtectedRoute
	if err := c.ShouldBindJSON(&route); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	route.ID = id
	if err := s.securityManager.UpdateProtectedRoute(&route); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"route": route})
}

// deleteProtectedRoute deletes a protected route
func (s *Server) deleteProtectedRoute(c *gin.Context) {
	if s.securityManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Security module not enabled"})
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid route ID"})
		return
	}

	if err := s.securityManager.DeleteProtectedRoute(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Route deleted successfully"})
}

// getDeploymentSecurity returns security settings for a deployment
func (s *Server) getDeploymentSecurity(c *gin.Context) {
	name := c.Param("name")

	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}

	securityConfig := deployment.Metadata.Security
	if securityConfig == nil {
		securityConfig = &models.DeploymentSecurityConfig{
			ProtectedPaths: []models.ProtectedPath{},
			RateLimits:     []models.DeploymentRateLimit{},
		}
	}

	c.JSON(http.StatusOK, gin.H{"security": securityConfig})
}

// updateDeploymentSecurity updates security settings for a deployment
func (s *Server) updateDeploymentSecurity(c *gin.Context) {
	name := c.Param("name")

	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}

	var securityConfig models.DeploymentSecurityConfig
	if err := c.ShouldBindJSON(&securityConfig); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if deployment.Metadata == nil {
		deployment.Metadata = &models.ServiceMetadata{}
	}
	deployment.Metadata.Security = &securityConfig

	if err := s.manager.SaveMetadata(name, deployment.Metadata); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"security": securityConfig})
}

// getDeploymentSecurityEvents returns security events for a deployment
func (s *Server) getDeploymentSecurityEvents(c *gin.Context) {
	if s.securityManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Security module not enabled"})
		return
	}

	name := c.Param("name")
	limit := 50
	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil {
			limit = parsed
		}
	}

	events, total, err := s.securityManager.GetEventsByDeployment(name, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"events":     events,
		"total":      total,
		"deployment": name,
	})
}

// getRealtimeCaptureStatus returns the current realtime capture status
func (s *Server) getRealtimeCaptureStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"enabled":          s.config.Security.Enabled,
		"realtime_capture": s.config.Security.RealtimeCapture,
	})
}

// setRealtimeCaptureStatus enables or disables realtime capture
func (s *Server) setRealtimeCaptureStatus(c *gin.Context) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := s.infraManager.SetNginxRealtimeCapture(req.Enabled); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	s.config.Security.RealtimeCapture = req.Enabled

	if s.configPath != "" {
		if err := config.Save(s.config, s.configPath); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Config updated but failed to save: " + err.Error()})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"realtime_capture": req.Enabled,
		"message":          "Realtime capture " + map[bool]string{true: "enabled", false: "disabled"}[req.Enabled],
	})
}
