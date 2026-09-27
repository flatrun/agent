package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/backup"
	"github.com/flatrun/agent/internal/scheduler"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

type deploymentBackupPolicy struct {
	Config         *backup.BackupSpec        `json:"config"`
	Schedules      []scheduler.ScheduledTask `json:"schedules"`
	BackupCount    int                       `json:"backup_count"`
	LocalBytes     int64                     `json:"local_bytes"`
	FailedCount    int                       `json:"failed_count"`
	SizeAlert      bool                      `json:"size_alert"`
	CleanupPreview *backup.CleanupPreview    `json:"cleanup_preview"`
}

func (s *Server) getDeploymentBackupPolicy(c *gin.Context) {
	name := c.Param("name")
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}
	spec := s.effectiveBackupSpec(deployment)
	backups, err := s.backupManager.ListBackups(&backup.BackupListFilter{DeploymentName: name})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	policy := deploymentBackupPolicy{Config: spec, Schedules: []scheduler.ScheduledTask{}, BackupCount: len(backups)}
	for _, item := range backups {
		if containsLocation(item.Locations, "local") {
			policy.LocalBytes += item.Size
		}
		if item.Status == backup.BackupStatusFailed || item.Status == backup.BackupStatusPartial || item.Status == backup.BackupStatusLocalOnly {
			policy.FailedCount++
		}
	}
	if s.schedulerManager != nil {
		tasks, taskErr := s.schedulerManager.GetTasksByDeployment(name)
		if taskErr == nil {
			for _, task := range tasks {
				if task.Type == scheduler.TaskTypeBackup {
					policy.Schedules = append(policy.Schedules, task)
				}
			}
		}
	}
	keep := spec.RetentionCount
	if keep < 1 {
		keep = 7
	}
	policy.CleanupPreview, _ = s.backupManager.PreviewCleanup(name, keep)
	policy.SizeAlert = spec.SizeAlertBytes > 0 && policy.LocalBytes >= spec.SizeAlertBytes
	c.JSON(http.StatusOK, gin.H{"policy": policy})
}

func containsLocation(locations []string, wanted string) bool {
	for _, location := range locations {
		if location == wanted {
			return true
		}
	}
	return false
}

func (s *Server) previewDeploymentBackupCleanup(c *gin.Context) {
	keep, err := strconv.Atoi(c.DefaultQuery("keep", "7"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid retention count"})
		return
	}
	preview, err := s.backupManager.PreviewCleanup(c.Param("name"), keep)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"preview": preview})
}

func (s *Server) cleanupDeploymentBackups(c *gin.Context) {
	var req struct {
		Keep int `json:"keep" binding:"required,min=1"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	deleted, err := s.backupManager.CleanupOldBackups(c.Param("name"), req.Keep)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": deleted})
}

func (s *Server) retryBackupPublication(c *gin.Context) {
	if s.backupManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Backup manager not enabled"})
		return
	}
	result, err := s.backupManager.RetryRemotePublication(context.Background(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"backup": result})
}

func (s *Server) listBackups(c *gin.Context) {
	if s.backupManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Backup manager not enabled"})
		return
	}

	filter := &backup.BackupListFilter{
		DeploymentName: c.Query("deployment"),
	}
	if filter.DeploymentName != "" {
		if !s.requireDeploymentAccess(c, filter.DeploymentName, auth.AccessLevelRead) {
			return
		}
	}

	if limit := c.Query("limit"); limit != "" {
		if l, err := strconv.Atoi(limit); err == nil {
			filter.Limit = l
		}
	}

	backups, err := s.backupManager.ListBackups(filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	actor := auth.GetActorFromContext(c)
	if actor != nil && actor.Role != auth.RoleAdmin {
		filtered := backups[:0]
		for _, b := range backups {
			if actor.CanAccessDeployment(b.DeploymentName, auth.AccessLevelRead) {
				filtered = append(filtered, b)
			}
		}
		backups = filtered
	}

	c.JSON(http.StatusOK, NewList(backups, "backups"))
}

func (s *Server) getBackup(c *gin.Context) {
	if s.backupManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Backup manager not enabled"})
		return
	}

	backupID := c.Param("id")
	b, err := s.backupManager.GetBackup(backupID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if !s.requireDeploymentAccess(c, b.DeploymentName, auth.AccessLevelRead) {
		return
	}

	c.JSON(http.StatusOK, gin.H{"backup": b})
}

func (s *Server) requireBackupDeployment(c *gin.Context) {
	if s.backupManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Backup manager not enabled"})
		c.Abort()
		return
	}
	b, err := s.backupManager.GetBackup(c.Param("id"))
	if err != nil || b.DeploymentName != c.Param("name") {
		c.JSON(http.StatusNotFound, gin.H{"error": "Backup not found"})
		c.Abort()
		return
	}
	c.Next()
}

func (s *Server) requireBackupJobDeployment(c *gin.Context) {
	if s.backupManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Backup manager not enabled"})
		c.Abort()
		return
	}
	job := s.backupManager.GetJob(c.Param("id"))
	if job == nil || job.DeploymentName != c.Param("name") {
		c.JSON(http.StatusNotFound, gin.H{"error": "Job not found"})
		c.Abort()
		return
	}
	c.Next()
}

func (s *Server) createBackup(c *gin.Context) {
	if s.backupManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Backup manager not enabled"})
		return
	}

	var req backup.CreateBackupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !s.requireDeploymentAccess(c, req.DeploymentName, auth.AccessLevelWrite) {
		return
	}

	deployment, err := s.manager.GetDeployment(req.DeploymentName)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Deployment not found: " + req.DeploymentName})
		return
	}

	spec := s.effectiveBackupSpec(deployment)

	jobID := s.backupManager.StartBackupJob(req.DeploymentName, spec)
	c.JSON(http.StatusAccepted, gin.H{"job_id": jobID, "message": "Backup job started"})
}

func (s *Server) createDeploymentBackup(c *gin.Context) {
	if s.backupManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Backup manager not enabled"})
		return
	}

	deploymentName := c.Param("name")
	deployment, err := s.manager.GetDeployment(deploymentName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}

	spec := s.effectiveBackupSpec(deployment)

	jobID := s.backupManager.StartBackupJob(deploymentName, spec)
	c.JSON(http.StatusAccepted, gin.H{"job_id": jobID, "message": "Backup job started"})
}

func (s *Server) listDeploymentBackups(c *gin.Context) {
	if s.backupManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Backup manager not enabled"})
		return
	}

	deploymentName := c.Param("name")

	filter := &backup.BackupListFilter{
		DeploymentName: deploymentName,
	}

	if limit := c.Query("limit"); limit != "" {
		if l, err := strconv.Atoi(limit); err == nil {
			filter.Limit = l
		}
	}

	backups, err := s.backupManager.ListBackups(filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, NewList(backups, "backups"))
}

func (s *Server) deleteBackup(c *gin.Context) {
	if s.backupManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Backup manager not enabled"})
		return
	}

	backupID := c.Param("id")
	b, err := s.backupManager.GetBackup(backupID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if !s.requireDeploymentAccess(c, b.DeploymentName, auth.AccessLevelWrite) {
		return
	}

	if err := s.backupManager.DeleteBackup(backupID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Backup deleted successfully"})
}

func (s *Server) downloadBackup(c *gin.Context) {
	if s.backupManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Backup manager not enabled"})
		return
	}

	backupID := c.Param("id")
	b, err := s.backupManager.GetBackup(backupID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if !s.requireDeploymentAccess(c, b.DeploymentName, auth.AccessLevelRead) {
		return
	}

	reader, size, err := s.backupManager.OpenBackupArchive(c.Request.Context(), backupID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	defer reader.Close()

	c.Header("Content-Description", "File Transfer")
	c.Header("Content-Disposition", "attachment; filename="+backupID+".tar.gz")
	c.DataFromReader(http.StatusOK, size, "application/gzip", reader, nil)
}

func (s *Server) getDeploymentBackupConfig(c *gin.Context) {
	deploymentName := c.Param("name")
	deployment, err := s.manager.GetDeployment(deploymentName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}

	spec := s.effectiveBackupSpec(deployment)

	c.JSON(http.StatusOK, gin.H{"backup_config": spec})
}

func (s *Server) updateDeploymentBackupConfig(c *gin.Context) {
	deploymentName := c.Param("name")
	deployment, err := s.manager.GetDeployment(deploymentName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}

	var spec backup.BackupSpec
	if err := c.ShouldBindJSON(&spec); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.validateBackupDestinations(spec.Destinations); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if spec.RetentionCount < 0 || spec.SizeAlertBytes < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Retention and size alert values cannot be negative"})
		return
	}

	if deployment.Metadata == nil {
		deployment.Metadata = &models.ServiceMetadata{}
	}
	deployment.Metadata.Backup = &spec

	if err := s.manager.SaveMetadata(deploymentName, deployment.Metadata); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"backup_config": spec})
}

func (s *Server) validateBackupDestinations(names []string) error {
	enabled := make(map[string]bool)
	for _, destination := range s.config.Backup.Destinations {
		if destination.IsEnabled() {
			enabled[destination.Name] = true
		}
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if name == "" || !enabled[name] {
			return fmt.Errorf("backup destination %q is unavailable", name)
		}
		if seen[name] {
			return fmt.Errorf("backup destination %q is selected more than once", name)
		}
		seen[name] = true
	}
	return nil
}

func (s *Server) restoreBackup(c *gin.Context) {
	if s.backupManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Backup manager not enabled"})
		return
	}

	backupID := c.Param("id")

	var req backup.RestoreBackupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		req = backup.RestoreBackupRequest{}
	}
	req.BackupID = backupID

	b, err := s.backupManager.GetBackup(backupID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	targetDeployment := b.DeploymentName
	if req.DeploymentName != "" {
		targetDeployment = req.DeploymentName
	}
	if !s.requireDeploymentAccess(c, b.DeploymentName, auth.AccessLevelRead) {
		return
	}
	if req.Isolated {
		if req.DeploymentName == "" || req.DeploymentName == b.DeploymentName {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Isolated restore requires a new deployment name"})
			return
		}
		actor := auth.GetActorFromContext(c)
		if actor != nil && !actor.HasPermission(auth.PermDeploymentsWrite) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Deployment write permission required"})
			return
		}
		if _, lookupErr := s.manager.GetDeployment(targetDeployment); lookupErr == nil {
			c.JSON(http.StatusConflict, gin.H{"error": "Deployment already exists"})
			return
		}
	} else if !s.requireDeploymentAccess(c, targetDeployment, auth.AccessLevelWrite) {
		return
	}

	actor := auth.GetActorFromContext(c)
	jobID := s.backupManager.StartRestoreJob(&req, func() error {
		if !req.Isolated || s.authManager == nil || actor == nil || actor.User == nil || actor.Role == auth.RoleAdmin {
			return nil
		}
		return s.authManager.AssignDeployment(actor.User.ID, targetDeployment, auth.AccessLevelAdmin, actor.User.ID)
	})
	c.JSON(http.StatusAccepted, gin.H{"job_id": jobID, "message": "Restore job started"})
}

func (s *Server) getBackupJob(c *gin.Context) {
	if s.backupManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Backup manager not enabled"})
		return
	}

	jobID := c.Param("id")
	job := s.backupManager.GetJob(jobID)
	if job == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Job not found"})
		return
	}
	if !s.requireDeploymentAccess(c, job.DeploymentName, auth.AccessLevelRead) {
		return
	}

	c.JSON(http.StatusOK, gin.H{"job": job})
}

func (s *Server) listBackupJobs(c *gin.Context) {
	if s.backupManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Backup manager not enabled"})
		return
	}

	deploymentName := c.Query("deployment")
	if deploymentName != "" {
		if !s.requireDeploymentAccess(c, deploymentName, auth.AccessLevelRead) {
			return
		}
	}
	limit := 50
	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil {
			limit = parsed
		}
	}

	jobs := s.backupManager.ListJobs(deploymentName, limit)
	actor := auth.GetActorFromContext(c)
	if actor != nil && actor.Role != auth.RoleAdmin {
		filtered := jobs[:0]
		for _, job := range jobs {
			if actor.CanAccessDeployment(job.DeploymentName, auth.AccessLevelRead) {
				filtered = append(filtered, job)
			}
		}
		jobs = filtered
	}

	c.JSON(http.StatusOK, gin.H{"jobs": jobs})
}
