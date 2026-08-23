package api

import (
	"net/http"
	"os/exec"
	"strings"

	"github.com/flatrun/agent/internal/auth"
	"github.com/gin-gonic/gin"
)

const composeProjectLabel = "com.docker.compose.project"

func (s *Server) requireDeploymentAccess(c *gin.Context, deploymentName, level string) bool {
	if deploymentName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Deployment name required"})
		return false
	}

	actor := auth.GetActorFromContext(c)
	// Nil actor is allowed for direct handler tests; production routes set an actor via auth middleware
	// or explicit anonymous-admin context when auth is disabled.
	if actor == nil {
		return true
	}
	if actor.Role == auth.RoleAdmin {
		return true
	}

	if !actor.CanAccessDeployment(deploymentName, level) {
		c.JSON(http.StatusForbidden, gin.H{"error": "No access to this deployment"})
		return false
	}

	return true
}

func restrictClusterServiceResources(c *gin.Context) {
	actor := auth.GetActorFromContext(c)
	if actor == nil || actor.User == nil || actor.User.Role != auth.RoleService || actor.User.Username != "__flatrun_cluster" {
		c.Next()
		return
	}

	path := c.Request.URL.Path
	if strings.HasPrefix(path, "/api/deployments/") || strings.HasPrefix(path, "/api/containers/") ||
		strings.HasPrefix(path, "/api/proxy/") {
		c.Next()
		return
	}
	for _, prefix := range []string{"/api/backups", "/api/certificates", "/api/credentials", "/api/images", "/api/security"} {
		if strings.HasPrefix(path, prefix) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Fleet credentials require a deployment-scoped endpoint"})
			c.Abort()
			return
		}
	}
	c.Next()
}

func (s *Server) requireContainerAccess(c *gin.Context, containerID, level string) bool {
	if containerID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Container ID required"})
		return false
	}

	actor := auth.GetActorFromContext(c)
	// Nil actor is allowed for direct handler tests; production routes set an actor via auth middleware
	// or explicit anonymous-admin context when auth is disabled.
	if actor == nil {
		return true
	}
	if actor.Role == auth.RoleAdmin {
		// Admins can see missing-container errors; non-admins below get a non-enumerating 403.
		if _, err := containerDeploymentName(containerID); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Container not found"})
			return false
		}
		return true
	}

	deploymentName, err := containerDeploymentName(containerID)
	if err != nil || deploymentName == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "No access to this container"})
		return false
	}

	if !actor.CanAccessDeployment(deploymentName, level) {
		c.JSON(http.StatusForbidden, gin.H{"error": "No access to this container"})
		return false
	}

	return true
}

func (s *Server) actorCanAccessContainer(c *gin.Context, containerID, level string) bool {
	actor := auth.GetActorFromContext(c)
	if actor == nil || actor.Role == auth.RoleAdmin {
		return true
	}

	deploymentName, err := containerDeploymentName(containerID)
	if err != nil || deploymentName == "" {
		return false
	}

	return actor.CanAccessDeployment(deploymentName, level)
}

func containerDeploymentName(containerID string) (string, error) {
	cmd := exec.Command("docker", "inspect", "--format", "{{ index .Config.Labels \""+composeProjectLabel+"\" }}", containerID)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	deploymentName := strings.TrimSpace(string(output))
	if deploymentName == "<no value>" {
		return "", nil
	}

	return deploymentName, nil
}
