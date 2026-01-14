package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/flatrun/agent/internal/audit"
	"github.com/gin-gonic/gin"
)

func (s *Server) listAuditEvents(c *gin.Context) {
	filter := &audit.AuditFilter{}

	if actorID := c.Query("actor_id"); actorID != "" {
		filter.ActorID = actorID
	}
	if actorType := c.Query("actor_type"); actorType != "" {
		filter.ActorType = audit.ActorType(actorType)
	}
	if action := c.Query("action"); action != "" {
		filter.Action = action
	}
	if resourceType := c.Query("resource_type"); resourceType != "" {
		filter.ResourceType = resourceType
	}
	if resourceID := c.Query("resource_id"); resourceID != "" {
		filter.ResourceID = resourceID
	}
	if successStr := c.Query("success"); successStr != "" {
		success := successStr == "true"
		filter.Success = &success
	}
	if clientIP := c.Query("client_ip"); clientIP != "" {
		filter.ClientIP = clientIP
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

	limit := 50
	if limitStr := c.Query("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 1000 {
			limit = l
		}
	}
	filter.Limit = limit

	if offsetStr := c.Query("offset"); offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			filter.Offset = o
		}
	}

	events, total, err := s.auditManager.GetEvents(filter)
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

func (s *Server) getAuditEvent(c *gin.Context) {
	idStr := c.Param("id")

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		event, err := s.auditManager.GetEventByEventID(idStr)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Event not found"})
			return
		}
		c.JSON(http.StatusOK, event)
		return
	}

	event, err := s.auditManager.GetEventByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Event not found"})
		return
	}

	c.JSON(http.StatusOK, event)
}

func (s *Server) getAuditStats(c *gin.Context) {
	stats, err := s.auditManager.GetStats()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, stats)
}

func (s *Server) exportAuditEvents(c *gin.Context) {
	var req struct {
		Format       string `json:"format"`
		ActorID      string `json:"actor_id"`
		ActorType    string `json:"actor_type"`
		Action       string `json:"action"`
		ResourceType string `json:"resource_type"`
		StartTime    string `json:"start_time"`
		EndTime      string `json:"end_time"`
		Limit        int    `json:"limit"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		req.Format = c.DefaultQuery("format", "json")
	}

	if req.Format == "" {
		req.Format = "json"
	}

	filter := &audit.AuditFilter{
		ActorID:      req.ActorID,
		Action:       req.Action,
		ResourceType: req.ResourceType,
		Limit:        req.Limit,
	}

	if req.ActorType != "" {
		filter.ActorType = audit.ActorType(req.ActorType)
	}
	if req.StartTime != "" {
		if t, err := time.Parse(time.RFC3339, req.StartTime); err == nil {
			filter.StartTime = t
		}
	}
	if req.EndTime != "" {
		if t, err := time.Parse(time.RFC3339, req.EndTime); err == nil {
			filter.EndTime = t
		}
	}
	if filter.Limit == 0 {
		filter.Limit = 10000
	}

	data, err := s.auditManager.ExportEvents(filter, req.Format)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	filename := "audit_export_" + time.Now().Format("20060102_150405")
	contentType := "application/json"
	if req.Format == "csv" {
		filename += ".csv"
		contentType = "text/csv"
	} else {
		filename += ".json"
	}

	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Data(http.StatusOK, contentType, data)
}

func (s *Server) cleanupAuditEvents(c *gin.Context) {
	deleted, err := s.auditManager.Cleanup()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Cleanup completed",
		"deleted": deleted,
	})
}
