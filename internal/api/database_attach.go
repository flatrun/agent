package api

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

func (s *Server) attachDeploymentDatabase(c *gin.Context) {
	name := c.Param("name")
	var req DatabaseConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}
	envVars, configs, err := s.createDatabasesForDeployment(name, []DatabaseConfigRequest{req})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	deployDir := filepath.Join(s.config.DeploymentsPath, name)
	var merged []EnvVar
	if current, readErr := os.ReadFile(filepath.Join(deployDir, ".env.flatrun")); readErr == nil {
		merged = parseEnvContent(string(current))
	}
	merged = mergeEnvVars(merged, envVars)
	if err := s.writeEnvFile(name, merged); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	composePath := filepath.Join(deployDir, "docker-compose.yml")
	content, err := os.ReadFile(composePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	updated, err := docker.EnsureServiceEnvFile(string(content), ".env.flatrun")
	if err == nil && (req.Mode == "shared" || req.Mode == "existing") {
		updated = s.addDatabaseNetwork(updated)
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := os.WriteFile(composePath, []byte(updated), 0644); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if deployment.Metadata == nil {
		deployment.Metadata = &models.ServiceMetadata{Name: name}
	}
	deployment.Metadata.Databases = append(deployment.Metadata.Databases, configs...)
	if err := s.manager.SaveMetadata(name, deployment.Metadata); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"databases": configs, "message": "Database attached. Restart the deployment to apply."})
}

func mergeEnvVars(current, additions []EnvVar) []EnvVar {
	index := make(map[string]int, len(current))
	for i, item := range current {
		index[item.Key] = i
	}
	for _, item := range additions {
		if i, ok := index[item.Key]; ok {
			current[i] = item
		} else {
			index[item.Key] = len(current)
			current = append(current, item)
		}
	}
	return current
}
