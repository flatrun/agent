package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// listContainerFiles lists a directory inside a running service's container.
func (s *Server) listContainerFiles(c *gin.Context) {
	name := c.Param("name")
	service := c.Param("service")

	dir := c.Query("path")
	if dir == "" {
		dir = "/"
	}

	project, err := s.manager.ComposeProject(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	files, err := s.manager.ListServiceFiles(ctx, project, service, dir)
	if err != nil {
		// An image without a shell cannot be browsed, but its paths can still be
		// brought onto the host by name, so say so rather than fail blankly.
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
			"hint":  "the service must be running and its image must provide a shell to be browsed",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{"path": dir, "service": service, "files": files})
}

// materializeContainerPath copies a path out of a running service onto the host
// and mounts it back, so the content becomes editable as ordinary files.
func (s *Server) materializeContainerPath(c *gin.Context) {
	name := c.Param("name")
	service := c.Param("service")

	if !s.requireUnprotectedDeploymentAction(c, name, protectedActionUpdateDeployment) {
		return
	}

	var req struct {
		ContainerPath string `json:"container_path" binding:"required"`
		HostPath      string `json:"host_path,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if !strings.HasPrefix(req.ContainerPath, "/") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "container_path must be absolute"})
		return
	}

	hostPath := req.HostPath
	if hostPath == "" {
		hostPath = defaultHostPathFor(req.ContainerPath)
	}

	if err := s.manager.MaterializeMount(name, service, req.ContainerPath, hostPath); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"deployment":     name,
		"service":        service,
		"container_path": req.ContainerPath,
		"host_path":      hostPath,
	})
}

// defaultHostPathFor names the host copy after the container path's last
// element, so /etc/nginx/conf.d becomes ./conf.d beside the compose file.
func defaultHostPathFor(containerPath string) string {
	trimmed := strings.Trim(containerPath, "/")
	if trimmed == "" {
		return ""
	}

	parts := strings.Split(trimmed, "/")
	return "./" + parts[len(parts)-1]
}
