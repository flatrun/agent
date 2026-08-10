package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

// streamInternalLogs hands a deployment's log lines to a built-in app as newline-delimited
// JSON. The user-facing stream is a websocket because a browser cannot set headers; an app
// can, so it gets the simpler transport and the same reader, which keeps log sources, the
// service filter and level parsing in one implementation.
func (s *Server) streamInternalLogs(c *gin.Context) {
	if s.pluginToken == "" || c.GetHeader("X-Plugin-Token") != s.pluginToken {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	name := c.Query("deployment")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "deployment required"})
		return
	}

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

	services, err := s.resolveLogServices(name, c.Query("service"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Replaying a day of history on every reconnect would re-raise handled incidents.
	tail := 0
	if v := c.Query("tail"); v != "" {
		if n, parseErr := strconv.Atoi(v); parseErr == nil && n >= 0 {
			tail = n
		}
	}

	c.Writer.Header().Set("Content-Type", "application/x-ndjson")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.WriteHeader(http.StatusOK)
	c.Writer.Flush()

	ctx := c.Request.Context()
	encoder := json.NewEncoder(c.Writer)

	sink := func(line string) {
		record := parseLogRecord(line)
		if source.Type == models.LogSourceFile && record.Service == "" {
			record.Service = source.Name
		}
		if err := encoder.Encode(logLine{Type: "log", Line: line, Record: record}); err != nil {
			return
		}
		c.Writer.Flush()
	}

	if source.Type == models.LogSourceFile {
		path, pathErr := resolveLogFilePath(deployment.Path, source.Path)
		if pathErr != nil {
			return
		}
		_ = streamFileLogs(ctx, path, tail, sink)
		return
	}
	_ = s.manager.StreamDeploymentLogs(ctx, name, deployment.Path, tail, sink, services...)
}
