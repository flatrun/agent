package api

import (
	"net/http"

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

	c.JSON(http.StatusOK, gin.H{
		"server": info,
	})
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
