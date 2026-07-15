package api

import (
	"net/http"

	"github.com/flatrun/agent/internal/dashboards"
	"github.com/gin-gonic/gin"
)

func (s *Server) listDashboards(c *gin.Context) {
	if s.dashboards == nil {
		c.JSON(http.StatusOK, gin.H{"dashboards": []dashboards.Dashboard{}})
		return
	}

	all := s.dashboards.List()
	if all == nil {
		all = []dashboards.Dashboard{}
	}
	c.JSON(http.StatusOK, gin.H{"dashboards": all})
}

func (s *Server) getDashboard(c *gin.Context) {
	if s.dashboards == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Dashboard not found"})
		return
	}

	d, ok := s.dashboards.Get(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Dashboard not found"})
		return
	}
	c.JSON(http.StatusOK, d)
}

// saveDashboard creates a dashboard, or replaces one when the body carries an id.
func (s *Server) saveDashboard(c *gin.Context) {
	if s.dashboards == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Dashboards not available"})
		return
	}

	var d dashboards.Dashboard
	if err := c.ShouldBindJSON(&d); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// A panel naming a series nothing produces is refused here rather than drawing an empty
	// chart forever.
	saved, err := s.dashboards.Save(d)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, saved)
}

func (s *Server) deleteDashboard(c *gin.Context) {
	if s.dashboards == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Dashboard not found"})
		return
	}

	removed, err := s.dashboards.Delete(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !removed {
		c.JSON(http.StatusNotFound, gin.H{"error": "Dashboard not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Dashboard deleted", "id": c.Param("id")})
}
