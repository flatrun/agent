package api

import (
	"net/http"
	"time"

	"github.com/flatrun/agent/internal/events"
	"github.com/gin-gonic/gin"
)

func (s *Server) listNotificationIncidents(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"incidents": s.notify.Incidents()})
}

func (s *Server) emitEvent(c *gin.Context) {
	if s.pluginToken == "" || c.GetHeader("X-Plugin-Token") != s.pluginToken {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var event events.Event
	if err := c.ShouldBindJSON(&event); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if event.Source == "" || event.Type == "" || event.Title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source, type, and title are required"})
		return
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	result, err := s.notify.Publish(event)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "result": result})
		return
	}
	c.JSON(http.StatusAccepted, result)
}
