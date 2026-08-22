package api

import (
	"net/http"
	"strings"

	"github.com/flatrun/agent/internal/system"
	"github.com/gin-gonic/gin"
)

func (s *Server) getServerInfo(c *gin.Context) {
	info, err := system.GetServerInfo()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}
	info.AgentURL = s.agentURL(c)

	c.JSON(http.StatusOK, gin.H{
		"server": info,
	})
}

func (s *Server) agentURL(c *gin.Context) string {
	if configured := strings.TrimRight(strings.TrimSpace(s.config.Cluster.AdvertiseURL), "/"); configured != "" {
		return configured
	}

	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	if forwarded := firstForwardedValue(c.GetHeader("X-Forwarded-Proto")); forwarded != "" {
		scheme = forwarded
	}

	host := c.Request.Host
	if forwarded := firstForwardedValue(c.GetHeader("X-Forwarded-Host")); forwarded != "" {
		host = forwarded
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

func firstForwardedValue(value string) string {
	value, _, _ = strings.Cut(value, ",")
	return strings.TrimSpace(value)
}

func (s *Server) getNetworkHealth(c *gin.Context) {
	health, err := system.CheckNetworkHealth(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"network_health": health,
	})
}
