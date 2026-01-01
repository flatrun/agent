package api

import (
	"net/http"
	"strconv"

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

	deployment, err := s.manager.GetDeployment(req.DeploymentName)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Deployment not found: " + req.DeploymentName})
		return
	}

	var spec *backup.BackupSpec
	if deployment.Metadata != nil && deployment.Metadata.Backup != nil {
		spec = deployment.Metadata.Backup
	}

	b, err := s.backupManager.CreateBackup(c.Request.Context(), req.DeploymentName, spec)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"backup": b})
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

	b, err := s.backupManager.CreateBackup(c.Request.Context(), deploymentName, spec)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"backup": b})
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
