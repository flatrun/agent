package api

import (
	"net/http"
	"strconv"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/backup"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

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

	c.JSON(http.StatusOK, gin.H{"backups": backups})
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

	var spec *backup.BackupSpec
	if deployment.Metadata != nil && deployment.Metadata.Backup != nil {
		spec = deployment.Metadata.Backup
	}

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

	var spec *backup.BackupSpec
	if deployment.Metadata != nil && deployment.Metadata.Backup != nil {
		spec = deployment.Metadata.Backup
	}

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

	c.JSON(http.StatusOK, gin.H{"backups": backups})
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

	backupPath, err := s.backupManager.GetBackupPath(backupID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.Header("Content-Description", "File Transfer")
	c.Header("Content-Disposition", "attachment; filename="+backupID+".tar.gz")
	c.Header("Content-Type", "application/gzip")
	c.File(backupPath)
}

func (s *Server) getDeploymentBackupConfig(c *gin.Context) {
	deploymentName := c.Param("name")
	deployment, err := s.manager.GetDeployment(deploymentName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}

	var spec *backup.BackupSpec
	if deployment.Metadata != nil && deployment.Metadata.Backup != nil {
		spec = deployment.Metadata.Backup
	}

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
	if !s.requireDeploymentAccess(c, targetDeployment, auth.AccessLevelWrite) {
		return
	}

	jobID := s.backupManager.StartRestoreJob(&req)
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
