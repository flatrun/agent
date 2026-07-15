package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/traffic"
	"github.com/gin-gonic/gin"
)

// ingestTrafficLog handles real-time traffic log ingestion from nginx Lua
func (s *Server) ingestTrafficLog(c *gin.Context) {
	if s.trafficManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Traffic logging not enabled"})
		return
	}

	var ingest traffic.IngestTrafficLog
	if err := c.ShouldBindJSON(&ingest); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	log, err := s.trafficManager.IngestLog(&ingest)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"logged": true, "id": log.ID})
}

// getTrafficLogs returns a paginated list of traffic logs
func (s *Server) getTrafficLogs(c *gin.Context) {
	if s.trafficManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Traffic logging not enabled"})
		return
	}

	filter := &traffic.TrafficFilter{
		DeploymentName: c.Query("deployment"),
		RequestMethod:  c.Query("method"),
		StatusGroup:    c.Query("status_group"),
		SourceIP:       c.Query("source_ip"),
		RequestPath:    c.Query("path"),
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

	if statusCode := c.Query("status_code"); statusCode != "" {
		if code, err := strconv.Atoi(statusCode); err == nil {
			filter.StatusCode = &code
		}
	}

	if limit := c.Query("limit"); limit != "" {
		if l, err := strconv.Atoi(limit); err == nil {
			filter.Limit = l
		}
	} else {
		filter.Limit = 100
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

	logs, total, err := s.trafficManager.GetLogs(filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"logs":   logs,
		"total":  total,
		"limit":  filter.Limit,
		"offset": filter.Offset,
	})
}

// getTrafficStats returns aggregated traffic statistics
func (s *Server) getTrafficStats(c *gin.Context) {
	if s.trafficManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Traffic logging not enabled"})
		return
	}

	deploymentName := c.Query("deployment")
	if deploymentName != "" {
		if !s.requireDeploymentAccess(c, deploymentName, auth.AccessLevelRead) {
			return
		}
	} else {
		actor := auth.GetActorFromContext(c)
		if actor != nil && actor.Role != auth.RoleAdmin {
			c.JSON(http.StatusForbidden, gin.H{"error": "Deployment filter required"})
			return
		}
	}

	since := 24 * time.Hour
	if sinceStr := c.Query("since"); sinceStr != "" {
		if d, err := time.ParseDuration(sinceStr); err == nil {
			since = d
		}
	}

	stats, err := s.trafficManager.GetStats(deploymentName, since)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"stats": stats})
}

func (s *Server) getUnknownDomainStats(c *gin.Context) {
	if s.trafficManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Traffic logging not enabled"})
		return
	}

	since := 24 * time.Hour
	if sinceStr := c.Query("since"); sinceStr != "" {
		if d, err := time.ParseDuration(sinceStr); err == nil {
			since = d
		}
	}

	deployments, err := s.manager.FindDeployments()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var knownDeployments []string
	for _, d := range deployments {
		knownDeployments = append(knownDeployments, d.Name)
	}

	stats, err := s.trafficManager.GetUnknownDomainStats(knownDeployments, since)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"stats": stats})
}

// cleanupTrafficLogs removes old traffic logs
func (s *Server) cleanupTrafficLogs(c *gin.Context) {
	if s.trafficManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Traffic logging not enabled"})
		return
	}

	var req struct {
		Days int `json:"days"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		req.Days = 7
	}
	if req.Days <= 0 {
		req.Days = 7
	}

	deleted, err := s.trafficManager.Cleanup(req.Days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"deleted": deleted})
}

// getDeploymentTrafficStats returns traffic stats for a specific deployment
func (s *Server) getDeploymentTrafficStats(c *gin.Context) {
	if s.trafficManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Traffic logging not enabled"})
		return
	}

	name := c.Param("name")

	since := 24 * time.Hour
	if sinceStr := c.Query("since"); sinceStr != "" {
		if d, err := time.ParseDuration(sinceStr); err == nil {
			since = d
		}
	}

	stats, err := s.trafficManager.GetStats(name, since)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"deployment": name,
		"stats":      stats,
	})
}
