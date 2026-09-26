package api

import (
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/contextkeys"
	"github.com/flatrun/agent/pkg/models"
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
	if !narrowClusterServiceDeployment(c, actor) {
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

func narrowClusterServiceDeployment(c *gin.Context, actor *auth.ActorContext) bool {
	deployment := strings.TrimSpace(c.GetHeader("X-FlatRun-Deployment"))
	if deployment == "" {
		return true
	}
	if actor.APIKey == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "A Fleet peer credential is required"})
		c.Abort()
		return false
	}

	level := ""
	for _, candidate := range []string{auth.AccessLevelAdmin, auth.AccessLevelWrite, auth.AccessLevelRead} {
		if actor.CanAccessDeployment(deployment, candidate) {
			level = candidate
			break
		}
	}
	if level == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "No access to this peer deployment"})
		c.Abort()
		return false
	}

	scopedActor := *actor
	scopedKey := *actor.APIKey
	scopedKey.Deployments = auth.DeploymentAccess{deployment: level}
	scopedActor.APIKey = &scopedKey
	c.Set(contextkeys.Actor, &scopedActor)
	return true
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
		if _, err := s.containerDeploymentName(containerID); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Container not found"})
			return false
		}
		return true
	}

	deploymentName, err := s.containerDeploymentName(containerID)
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

	deploymentName, err := s.containerDeploymentName(containerID)
	if err != nil || deploymentName == "" {
		return false
	}

	return actor.CanAccessDeployment(deploymentName, level)
}

func inspectContainerIdentity(containerID string) (string, string, string, error) {
	format := "{{.Id}}\n{{.Name}}\n{{ index .Config.Labels \"" + composeProjectLabel + "\" }}"
	cmd := exec.Command("docker", "inspect", "--format", format, containerID)
	output, err := cmd.Output()
	if err != nil {
		return "", "", "", err
	}
	parts := strings.SplitN(strings.TrimSpace(string(output)), "\n", 3)
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("unexpected container inspection result")
	}
	canonicalID := strings.TrimSpace(parts[0])
	containerName := strings.TrimPrefix(strings.TrimSpace(parts[1]), "/")
	deploymentName := strings.TrimSpace(parts[2])
	if deploymentName == "<no value>" {
		deploymentName = ""
	}
	return canonicalID, containerName, deploymentName, nil
}

func (s *Server) containerDeploymentName(containerID string) (string, error) {
	canonicalID, _, label, inspectErr := inspectContainerIdentity(containerID)
	if s.manager == nil {
		return label, inspectErr
	}
	if label != "" {
		if deployment, err := s.manager.GetDeployment(label); err == nil && deploymentContainsContainer(deployment, canonicalID) {
			return deployment.Name, nil
		}
	}
	deployments, err := s.manager.FindDeployments()
	if err != nil {
		return "", err
	}
	for _, candidate := range deployments {
		deployment, getErr := s.manager.GetDeployment(candidate.Name)
		if getErr == nil && deploymentContainsContainer(deployment, canonicalID) {
			return deployment.Name, nil
		}
	}
	if inspectErr != nil {
		return "", inspectErr
	}
	return "", fmt.Errorf("container does not belong to a deployment")
}

func deploymentContainsContainer(deployment *models.Deployment, containerID string) bool {
	for _, service := range deployment.Services {
		if len(service.ContainerID) < 12 || len(containerID) < 12 {
			continue
		}
		if service.ContainerID == containerID || strings.HasPrefix(service.ContainerID, containerID) || strings.HasPrefix(containerID, service.ContainerID) {
			return true
		}
	}
	return false
}
