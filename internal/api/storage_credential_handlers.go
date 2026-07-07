package api

import (
	"fmt"
	"net/http"

	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

func (s *Server) listStorageCredentials(c *gin.Context) {
	kind := models.CredentialKind(c.Query("kind"))
	creds := s.credentialsManager.ListGenericCredentials(kind)
	c.JSON(http.StatusOK, gin.H{"credentials": creds})
}

func (s *Server) createStorageCredential(c *gin.Context) {
	var req struct {
		Name string            `json:"name" binding:"required"`
		Kind string            `json:"kind" binding:"required"`
		Data map[string]string `json:"data"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	kind := models.CredentialKind(req.Kind)
	if err := validateStorageCredentialData(kind, req.Data, true); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	cred, err := s.credentialsManager.CreateGenericCredential(req.Name, kind, req.Data)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "Credential created", "credential": cred})
}

func (s *Server) updateStorageCredential(c *gin.Context) {
	id := c.Param("id")

	var req struct {
		Name string            `json:"name"`
		Data map[string]string `json:"data"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	cred, err := s.credentialsManager.UpdateGenericCredential(id, req.Name, req.Data)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Credential updated", "credential": cred})
}

func (s *Server) deleteStorageCredential(c *gin.Context) {
	id := c.Param("id")

	for _, dest := range s.config.Backup.Destinations {
		if dest.CredentialID == id {
			c.JSON(http.StatusConflict, gin.H{"error": "credential is in use by backup destination " + dest.Name})
			return
		}
	}

	if err := s.credentialsManager.DeleteGenericCredential(id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Credential deleted", "id": id})
}

func validateStorageCredentialData(kind models.CredentialKind, data map[string]string, requireSecret bool) error {
	switch kind {
	case models.CredentialKindS3:
		if data["access_key_id"] == "" {
			return fmt.Errorf("access_key_id is required")
		}
		if requireSecret && data["secret_access_key"] == "" {
			return fmt.Errorf("secret_access_key is required")
		}
		return nil
	default:
		return fmt.Errorf("unknown credential kind: %s", kind)
	}
}
