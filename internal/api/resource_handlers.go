package api

import (
	"net/http"

	"github.com/flatrun/agent/internal/docker"
	"github.com/gin-gonic/gin"
)

func (s *Server) getContainerResources(c *gin.Context) {
	id := c.Param("id")

	resources, err := docker.GetContainerResources(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"resources": resources,
	})
}

func (s *Server) updateContainerResources(c *gin.Context) {
	id := c.Param("id")

	var update docker.ResourceUpdate
	if err := c.ShouldBindJSON(&update); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid request body: " + err.Error(),
		})
		return
	}

	if update.MemoryLimit == nil && update.MemorySwap == nil &&
		update.CPUs == nil && update.CPUShares == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "At least one resource limit must be specified",
		})
		return
	}

	if err := docker.UpdateContainerResources(id, &update); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	resources, _ := docker.GetContainerResources(id)

	c.JSON(http.StatusOK, gin.H{
		"message":   "Resources updated",
		"resources": resources,
	})
}

func (s *Server) getDeploymentResources(c *gin.Context) {
	name := c.Param("name")

	if _, err := s.manager.GetDeployment(name); err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "deployment not found",
		})
		return
	}

	resources, err := docker.GetDeploymentResources(name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"deployment": name,
		"resources":  resources,
	})
}
