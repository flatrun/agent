package api

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/flatrun/agent/internal/infra"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

// truncateContainerLog empties what Docker has stored for a container.
//
// Docker keeps the log itself, so there is nothing to delete through the API: the file behind
// LogPath is truncated in place. That keeps the container running and its file descriptor
// valid, which deleting the file would not.
func (s *Server) truncateContainerLog(container string) error {
	if strings.TrimSpace(container) == "" {
		return fmt.Errorf("no container to clear")
	}

	path, err := s.manager.ContainerLogPath(container)
	if err != nil {
		return fmt.Errorf("could not find the log for %s: %w", container, err)
	}

	path = strings.TrimSpace(path)
	if path == "" {
		// A container on journald, syslog or a remote driver has no file here to empty, and
		// clearing whatever it does write is that system's business, not FlatRun's.
		return fmt.Errorf("%s does not log to a file Docker owns, so there is nothing here to clear", container)
	}
	if err := os.Truncate(path, 0); err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("the agent is not allowed to clear %s: %w", path, err)
		}
		return err
	}
	return nil
}

// deleteDeploymentLogs empties the log the viewer is reading: the file for a file source, or
// what Docker holds for the containers behind container output.
func (s *Server) deleteDeploymentLogs(c *gin.Context) {
	name := c.Param("name")

	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}

	source, ok := resolveLogSource(deployment.Metadata, c.Query("source"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown log source"})
		return
	}

	// Checked against the compose file for the same reason reading is: a name that matches
	// nothing would otherwise report success while emptying nothing.
	wantedServices, err := s.resolveLogServices(name, c.Query("service"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if source.Type == models.LogSourceFile {
		path, pathErr := resolveLogFilePath(deployment.Path, source.Path)
		if pathErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": pathErr.Error()})
			return
		}
		if truncErr := os.Truncate(path, 0); truncErr != nil && !os.IsNotExist(truncErr) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": truncErr.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Log cleared", "source": source.ID})
		return
	}
	if source.Type == models.LogSourceContainerFile {
		_, truncErr := s.manager.ComposeExec(c.Request.Context(), name, source.Service, ": > "+shellLiteral(source.Path))
		if truncErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": truncErr.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Log cleared", "source": source.ID})
		return
	}

	services, err := s.manager.GetComposeServices(name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var failures []string
	cleared := 0
	for _, svc := range services {
		if len(wantedServices) > 0 && wantedServices[0] != svc.Name {
			continue
		}
		if svc.ContainerID == "" {
			continue
		}
		if truncErr := s.truncateContainerLog(svc.ContainerID); truncErr != nil {
			failures = append(failures, truncErr.Error())
			continue
		}
		cleared++
	}

	if cleared == 0 && len(failures) > 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"error": strings.Join(failures, "; ")})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message":  "Log cleared",
		"source":   source.ID,
		"cleared":  cleared,
		"warnings": failures,
	})
}

// deleteSystemLogs empties what Docker holds for a system service's container.
func (s *Server) deleteSystemLogs(c *gin.Context) {
	if s.infraManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Infrastructure not available"})
		return
	}

	src, ok := s.resolveSystemLogSource(c.Query("source"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown log source"})
		return
	}

	container := s.infraManager.ContainerName(src.Service)
	if container == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown service"})
		return
	}

	// Access and error share one container, so clearing either clears both. Saying so is
	// better than quietly emptying more than was asked for.
	if err := s.truncateContainerLog(container); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	message := "Log cleared"
	if src.Stream != infra.LogStreamAll {
		message = "Log cleared, including the other stream from the same container"
	}
	c.JSON(http.StatusOK, gin.H{"message": message, "source": src.ID})
}
