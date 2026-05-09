package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"time"

	"github.com/flatrun/agent/internal/auth"
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
		clientIP := c.ClientIP()
		log.Printf("Security ingest: failed to parse JSON from %s: %v", clientIP, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result, err := s.securityManager.IngestEvent(&event, s.config.Security.AutoBlockDuration)
	if err != nil {
		log.Printf("Security ingest: failed to process event from IP %s (path=%s, method=%s): %v",
			event.SourceIP, event.RequestPath, event.RequestMethod, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if result.Event == nil {
		c.JSON(http.StatusOK, gin.H{"processed": false, "reason": "Event not security-relevant or IP blocked"})
		return
	}

	// If an IP was auto-blocked, notify nginx immediately
	if result.AutoBlocked {
		if err := s.notifyNginxBlockIP(result.BlockedIP, result.BlockTTL); err != nil {
			log.Printf("Warning: failed to notify nginx about auto-blocked IP %s: %v", result.BlockedIP, err)
		}
	}

	c.JSON(http.StatusCreated, gin.H{
		"processed":    true,
		"event":        result.Event,
		"auto_blocked": result.AutoBlocked,
	})
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
	if filter.DeploymentName != "" {
		if !s.requireDeploymentAccess(c, filter.DeploymentName, auth.AccessLevelRead) {
			return
		}
	} else {
		actor := auth.GetActorFromContext(c)
		if actor != nil && actor.Role != auth.RoleAdmin {
			c.JSON(http.StatusForbidden, gin.H{"error": "Deployment filter required"})
			return
		}
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
	if event.DeploymentName != "" && !s.requireDeploymentAccess(c, event.DeploymentName, auth.AccessLevelRead) {
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

// listBlockedIPsInternal returns blocked IPs for internal nginx communication
func (s *Server) listBlockedIPsInternal(c *gin.Context) {
	token := c.GetHeader("X-Internal-Token")
	expectedToken := s.config.Security.InternalAPIToken

	if token == "" || token != expectedToken {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid internal token"})
		return
	}

	s.listBlockedIPs(c)
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

	// Notify nginx to immediately block the IP
	if err := s.notifyNginxBlockIP(req.IP, req.Duration); err != nil {
		log.Printf("Warning: failed to notify nginx about blocked IP %s: %v", req.IP, err)
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

	// Notify nginx to immediately unblock the IP
	if err := s.notifyNginxUnblockIP(ip); err != nil {
		log.Printf("Warning: failed to notify nginx about unblocked IP %s: %v", ip, err)
	}

	c.JSON(http.StatusOK, gin.H{"message": "IP unblocked successfully"})
}

func (s *Server) listWhitelist(c *gin.Context) {
	if s.securityManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Security module not enabled"})
		return
	}

	entries, err := s.securityManager.GetWhitelist()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"whitelist": entries})
}

func (s *Server) addWhitelistEntry(c *gin.Context) {
	if s.securityManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Security module not enabled"})
		return
	}

	var req struct {
		Value  string `json:"value" binding:"required"`
		Type   string `json:"type" binding:"required"`
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Type != "ip" && req.Type != "cidr" && req.Type != "path" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Type must be 'ip', 'cidr', or 'path'"})
		return
	}

	id, err := s.securityManager.AddWhitelistEntry(req.Value, req.Type, req.Reason)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": id})
}

func (s *Server) removeWhitelistEntry(c *gin.Context) {
	if s.securityManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Security module not enabled"})
		return
	}

	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ID"})
		return
	}

	if err := s.securityManager.RemoveWhitelistEntry(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Entry removed"})
}

func (s *Server) listWhitelistInternal(c *gin.Context) {
	token := c.GetHeader("X-Internal-Token")
	expectedToken := s.config.Security.InternalAPIToken

	if token == "" || token != expectedToken {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid internal token"})
		return
	}

	s.listWhitelist(c)
}

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

	var securityConfig *models.DeploymentSecurityConfig
	if deployment.Metadata != nil && deployment.Metadata.Security != nil {
		securityConfig = deployment.Metadata.Security
	} else {
		securityConfig = &models.DeploymentSecurityConfig{
			Enabled:        false,
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

	vhostUpdated := false
	var hookStatus interface{}

	if s.proxyOrchestrator != nil && s.proxyOrchestrator.NginxManager().VirtualHostExists(name) {
		nginxMgr := s.proxyOrchestrator.NginxManager()

		if err := nginxMgr.UpdateVirtualHost(deployment); err != nil {
			c.JSON(http.StatusOK, gin.H{
				"security":      securityConfig,
				"vhost_updated": false,
				"warning":       "Security config saved but vhost update failed: " + err.Error(),
			})
			return
		}

		if err := nginxMgr.ValidateSecurityHooks(name, securityConfig.Enabled); err != nil {
			c.JSON(http.StatusOK, gin.H{
				"security":         securityConfig,
				"vhost_updated":    true,
				"validation_error": err.Error(),
				"warning":          "Vhost updated but security hook validation failed",
			})
			return
		}

		hookStatus, _ = nginxMgr.GetSecurityHookStatus(name)

		if err := nginxMgr.TestConfig(); err != nil {
			c.JSON(http.StatusOK, gin.H{
				"security":      securityConfig,
				"vhost_updated": true,
				"hook_status":   hookStatus,
				"warning":       "Security config saved but nginx config test failed: " + err.Error(),
			})
			return
		}

		if err := nginxMgr.Reload(); err != nil {
			c.JSON(http.StatusOK, gin.H{
				"security":      securityConfig,
				"vhost_updated": true,
				"hook_status":   hookStatus,
				"warning":       "Nginx reload failed (may need manual reload): " + err.Error(),
			})
			return
		}
		vhostUpdated = true
	}

	response := gin.H{
		"security":      securityConfig,
		"vhost_updated": vhostUpdated,
	}
	if hookStatus != nil {
		response["hook_status"] = hookStatus
	}

	c.JSON(http.StatusOK, response)
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

	// Check prerequisites before attempting to enable
	if req.Enabled {
		if s.config.Nginx.ContainerName == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":            "Nginx container name not configured",
				"realtime_capture": s.config.Security.RealtimeCapture,
				"details":          "Set nginx.container_name in config to enable realtime capture",
			})
			return
		}

		// Check if nginx container is running
		if !s.infraManager.IsNginxRunning() {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error":            "Nginx container is not running",
				"realtime_capture": s.config.Security.RealtimeCapture,
				"details":          "Start the nginx/proxy infrastructure before enabling realtime capture",
			})
			return
		}
	}

	result, err := s.infraManager.SetNginxRealtimeCaptureWithStatus(req.Enabled)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":            err.Error(),
			"realtime_capture": s.config.Security.RealtimeCapture,
			"details":          result,
		})
		return
	}

	s.config.Security.RealtimeCapture = req.Enabled

	if s.configPath != "" {
		if err := config.Save(s.config, s.configPath); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":            "Lua configs updated but failed to save agent config: " + err.Error(),
				"realtime_capture": req.Enabled,
			})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"realtime_capture": req.Enabled,
		"message":          "Realtime capture " + map[bool]string{true: "enabled", false: "disabled"}[req.Enabled],
		"details":          result,
	})
}

// getSecurityHealth returns the health status of the security setup
func (s *Server) getSecurityHealth(c *gin.Context) {
	if s.securityManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "disabled",
			"error":  "Security module not enabled",
			"checks": map[string]bool{},
			"issues": []string{"Security is not enabled in agent configuration"},
			"recommendations": []string{
				"Set security.enabled: true in config.yml",
				"Restart the agent after updating config",
			},
		})
		return
	}

	health := s.infraManager.CheckSecurityHealth()
	c.JSON(http.StatusOK, health)
}

// updateSecuritySettings handles dedicated security settings updates
func (s *Server) updateSecuritySettings(c *gin.Context) {
	var req struct {
		Enabled            *bool  `json:"enabled"`
		RealtimeCapture    *bool  `json:"realtime_capture"`
		ScanInterval       string `json:"scan_interval"`
		RetentionDays      int    `json:"retention_days"`
		RateThreshold      int    `json:"rate_threshold"`
		AutoBlockEnabled   *bool  `json:"auto_block_enabled"`
		AutoBlockThreshold int    `json:"auto_block_threshold"`
		AutoBlockDuration  string `json:"auto_block_duration"`
		// Detection thresholds
		DetectionWindow       string `json:"detection_window"`
		NotFoundThreshold     int    `json:"not_found_threshold"`
		AuthFailureThreshold  int    `json:"auth_failure_threshold"`
		UniquePathsThreshold  int    `json:"unique_paths_threshold"`
		RepeatedHitsThreshold int    `json:"repeated_hits_threshold"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result := gin.H{
		"updated_fields": []string{},
	}
	var updatedFields []string

	// Track previous enabled state for nginx action
	prevEnabled := s.config.Security.Enabled

	// Update enabled state - this controls nginx Lua setup
	if req.Enabled != nil && *req.Enabled != s.config.Security.Enabled {
		s.config.Security.Enabled = *req.Enabled
		updatedFields = append(updatedFields, "enabled")
	}

	// Update realtime capture (kept for config compatibility)
	if req.RealtimeCapture != nil && *req.RealtimeCapture != s.config.Security.RealtimeCapture {
		s.config.Security.RealtimeCapture = *req.RealtimeCapture
		updatedFields = append(updatedFields, "realtime_capture")
	}

	// Nginx action only when enabled state changes
	needsNginxAction := s.config.Security.Enabled != prevEnabled

	// Update other settings
	if req.ScanInterval != "" {
		if d, err := time.ParseDuration(req.ScanInterval); err == nil {
			s.config.Security.ScanInterval = d
			updatedFields = append(updatedFields, "scan_interval")
		}
	}
	if req.RetentionDays > 0 {
		s.config.Security.RetentionDays = req.RetentionDays
		updatedFields = append(updatedFields, "retention_days")
	}
	if req.RateThreshold > 0 {
		s.config.Security.RateThreshold = req.RateThreshold
		updatedFields = append(updatedFields, "rate_threshold")
	}
	if req.AutoBlockEnabled != nil {
		s.config.Security.AutoBlockEnabled = *req.AutoBlockEnabled
		updatedFields = append(updatedFields, "auto_block_enabled")
	}
	if req.AutoBlockThreshold > 0 {
		s.config.Security.AutoBlockThreshold = req.AutoBlockThreshold
		updatedFields = append(updatedFields, "auto_block_threshold")
	}
	if req.AutoBlockDuration != "" {
		if d, err := time.ParseDuration(req.AutoBlockDuration); err == nil {
			s.config.Security.AutoBlockDuration = d
			updatedFields = append(updatedFields, "auto_block_duration")
		}
	}
	// Detection thresholds
	if req.DetectionWindow != "" {
		if d, err := time.ParseDuration(req.DetectionWindow); err == nil {
			s.config.Security.DetectionWindow = d
			updatedFields = append(updatedFields, "detection_window")
		}
	}
	if req.NotFoundThreshold > 0 {
		s.config.Security.NotFoundThreshold = req.NotFoundThreshold
		updatedFields = append(updatedFields, "not_found_threshold")
	}
	if req.AuthFailureThreshold > 0 {
		s.config.Security.AuthFailureThreshold = req.AuthFailureThreshold
		updatedFields = append(updatedFields, "auth_failure_threshold")
	}
	if req.UniquePathsThreshold > 0 {
		s.config.Security.UniquePathsThreshold = req.UniquePathsThreshold
		updatedFields = append(updatedFields, "unique_paths_threshold")
	}
	if req.RepeatedHitsThreshold > 0 {
		s.config.Security.RepeatedHitsThreshold = req.RepeatedHitsThreshold
		updatedFields = append(updatedFields, "repeated_hits_threshold")
	}

	result["updated_fields"] = updatedFields

	// Perform nginx action if needed
	if needsNginxAction {
		// Check prerequisites when enabling
		if s.config.Security.Enabled {
			if s.config.Nginx.ContainerName == "" {
				c.JSON(http.StatusBadRequest, gin.H{
					"error":   "Nginx container name not configured",
					"details": "Set nginx.container_name in config to enable security",
				})
				return
			}
			if !s.infraManager.IsNginxRunning() {
				c.JSON(http.StatusServiceUnavailable, gin.H{
					"error":   "Nginx container is not running",
					"details": "Start the nginx/proxy infrastructure before enabling security",
				})
				return
			}
		}

		actionResult, err := s.infraManager.SetNginxRealtimeCaptureWithStatus(s.config.Security.Enabled)
		result["nginx_action"] = actionResult

		if err != nil {
			result["nginx_error"] = err.Error()
		}
	}

	// Save config
	if s.configPath != "" {
		if err := config.Save(s.config, s.configPath); err != nil {
			result["config_save_error"] = err.Error()
		} else {
			result["config_saved"] = true
		}
	}

	// Update dependent managers
	s.infraManager.UpdateConfig(s.config)

	// Update detector thresholds if security manager is available
	if s.securityManager != nil {
		s.securityManager.SetDetectorThresholds(
			s.config.Security.RateThreshold,
			s.config.Security.NotFoundThreshold,
			s.config.Security.AuthFailureThreshold,
			s.config.Security.UniquePathsThreshold,
			s.config.Security.RepeatedHitsThreshold,
			s.config.Security.DetectionWindow,
		)
	}

	// Return current security settings
	result["security"] = gin.H{
		"enabled":                 s.config.Security.Enabled,
		"realtime_capture":        s.config.Security.RealtimeCapture,
		"scan_interval":           s.config.Security.ScanInterval.String(),
		"retention_days":          s.config.Security.RetentionDays,
		"rate_threshold":          s.config.Security.RateThreshold,
		"auto_block_enabled":      s.config.Security.AutoBlockEnabled,
		"auto_block_threshold":    s.config.Security.AutoBlockThreshold,
		"auto_block_duration":     s.config.Security.AutoBlockDuration.String(),
		"detection_window":        s.config.Security.DetectionWindow.String(),
		"not_found_threshold":     s.config.Security.NotFoundThreshold,
		"auth_failure_threshold":  s.config.Security.AuthFailureThreshold,
		"unique_paths_threshold":  s.config.Security.UniquePathsThreshold,
		"repeated_hits_threshold": s.config.Security.RepeatedHitsThreshold,
	}

	c.JSON(http.StatusOK, result)
}

// notifyNginxBlockIP notifies nginx to immediately add an IP to its blocked list
func (s *Server) notifyNginxBlockIP(ip string, ttlSeconds int) error {
	if s.config.Nginx.ContainerName == "" {
		return fmt.Errorf("nginx container name not configured")
	}

	if ttlSeconds <= 0 {
		ttlSeconds = 86400 * 365 // 1 year for permanent blocks
	}

	payload := map[string]interface{}{
		"ip":  ip,
		"ttl": ttlSeconds,
	}
	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	curlCmd := fmt.Sprintf(
		`curl -s -X POST -H "Content-Type: application/json" -d '%s' http://127.0.0.1:8081/_internal/security/block-ip`,
		string(jsonPayload),
	)

	cmd := exec.Command("docker", "exec", s.config.Nginx.ContainerName, "sh", "-c", curlCmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to notify nginx: %s - %w", string(output), err)
	}

	log.Printf("Notified nginx to block IP %s (ttl=%ds): %s", ip, ttlSeconds, string(output))
	return nil
}

// notifyNginxUnblockIP notifies nginx to immediately remove an IP from its blocked list
func (s *Server) notifyNginxUnblockIP(ip string) error {
	if s.config.Nginx.ContainerName == "" {
		return fmt.Errorf("nginx container name not configured")
	}

	payload := map[string]interface{}{
		"ip": ip,
	}
	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	curlCmd := fmt.Sprintf(
		`curl -s -X POST -H "Content-Type: application/json" -d '%s' http://127.0.0.1:8081/_internal/security/unblock-ip`,
		string(jsonPayload),
	)

	cmd := exec.Command("docker", "exec", s.config.Nginx.ContainerName, "sh", "-c", curlCmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to notify nginx: %s - %w", string(output), err)
	}

	log.Printf("Notified nginx to unblock IP %s: %s", ip, string(output))
	return nil
}

// refreshSecurityScripts regenerates Lua scripts with correct agent IP and reloads nginx
func (s *Server) refreshSecurityScripts(c *gin.Context) {
	if !s.config.Security.Enabled {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Security module not enabled",
		})
		return
	}

	if !s.infraManager.IsNginxRunning() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Nginx container is not running",
		})
		return
	}

	result, err := s.infraManager.RefreshSecurityScripts()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":  err.Error(),
			"result": result,
		})
		return
	}

	c.JSON(http.StatusOK, result)
}
