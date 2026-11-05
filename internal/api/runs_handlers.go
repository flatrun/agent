package api

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/runs"
	"github.com/gin-gonic/gin"
)

func (s *Server) registerRunRoutes(group *gin.RouterGroup) {
	group.POST("/runs", s.authMiddleware.RequirePermission(auth.PermRunsWrite), s.createRun)
	group.GET("/runs", s.authMiddleware.RequirePermission(auth.PermRunsRead), s.listRuns)
	group.GET("/runs/:id", s.authMiddleware.RequirePermission(auth.PermRunsRead), s.getRun)
	group.GET("/runs/:id/logs", s.authMiddleware.RequirePermission(auth.PermRunsRead), s.runLogs)
	group.GET("/runs/:id/artifacts/:name", s.authMiddleware.RequirePermission(auth.PermRunsRead), s.runArtifact)
	group.DELETE("/runs/:id", s.authMiddleware.RequirePermission(auth.PermRunsWrite), s.cancelRun)
}

func (s *Server) runsAvailable(c *gin.Context) bool {
	if s.runsManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "runs unavailable: runtime or run storage could not be initialized"})
		return false
	}
	return true
}

func hasGlobalRunAccess(actor *auth.ActorContext) bool {
	return actor.Role == auth.RoleAdmin && (actor.APIKey == nil || len(actor.APIKey.Deployments) == 0)
}

func (s *Server) visibleRun(c *gin.Context) *runs.Run {
	if !s.runsAvailable(c) {
		return nil
	}
	run, err := s.runsManager.Store.Get(c.Param("id"))
	actor := auth.GetActorFromContext(c)
	if err != nil || actor == nil || (!hasGlobalRunAccess(actor) && run.OwnerUserID != actor.UserID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
		return nil
	}
	return run
}

func (s *Server) createRun(c *gin.Context) {
	if !s.runsAvailable(c) {
		return
	}
	var request struct {
		runs.Spec
		SecretEnv map[string]string `json:"secret_env"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	if c.ShouldBindJSON(&request) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid run request"})
		return
	}
	defer clear(request.SecretEnv)
	if err := request.Spec.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	actor := auth.GetActorFromContext(c)
	username := ""
	if actor.User != nil {
		username = actor.User.Username
	}
	run, err := s.runsManager.Submit(actor.UserID, username, request.Spec, request.SecretEnv)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, runs.ErrQueueFull) || errors.Is(err, runs.ErrClosed) {
			status = http.StatusServiceUnavailable
		}
		c.JSON(status, gin.H{"error": "unable to queue run"})
		return
	}
	c.JSON(http.StatusAccepted, run)
}

func (s *Server) listRuns(c *gin.Context) {
	if !s.runsAvailable(c) {
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if err != nil || limit < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be a positive integer"})
		return
	}
	if limit > 500 {
		limit = 500
	}
	actor := auth.GetActorFromContext(c)
	var owner *int64
	if !hasGlobalRunAccess(actor) {
		owner = &actor.UserID
	}
	records, err := s.runsManager.Store.List(owner, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "cannot list runs"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": records})
}

func (s *Server) getRun(c *gin.Context) {
	if run := s.visibleRun(c); run != nil {
		c.JSON(http.StatusOK, run)
	}
}

func (s *Server) cancelRun(c *gin.Context) {
	run := s.visibleRun(c)
	if run == nil {
		return
	}
	if err := s.runsManager.Cancel(run.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "cannot cancel run"})
		return
	}
	run, err := s.runsManager.Store.Get(run.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "cannot read cancelled run"})
		return
	}
	c.JSON(http.StatusOK, run)
}

func (s *Server) runArtifact(c *gin.Context) {
	run := s.visibleRun(c)
	if run == nil {
		return
	}
	for _, artifact := range run.Artifacts {
		if artifact.Name == c.Param("name") {
			c.Header("Content-Type", "application/x-tar")
			c.FileAttachment(s.runsManager.ArtifactPath(run.ID, artifact.Name), artifact.Name)
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "artifact not found"})
}

func (s *Server) runLogs(c *gin.Context) {
	run := s.visibleRun(c)
	if run == nil {
		return
	}
	follow := c.Query("follow") == "true"
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Status(http.StatusOK)
	var offset int64
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	for {
		output, err := os.Open(s.runsManager.LogPath(run.ID))
		if err == nil {
			_, _ = output.Seek(offset, io.SeekStart)
			size, _ := io.Copy(c.Writer, output)
			offset += size
			output.Close()
		}
		c.Writer.Flush()
		if !follow || run.Status.Finished() {
			return
		}
		select {
		case <-c.Request.Context().Done():
			return
		case <-ticker.C:
		}
		run, err = s.runsManager.Store.Get(run.ID)
		if err != nil {
			return
		}
	}
}
