package api

import (
	"net/http"

	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

// listSourceCredentials returns the saved credentials for reaching a private
// source (git today), so a deployment can reuse a stored token instead of the
// operator pasting one each time. Tokens are masked in the response.
func (s *Server) listSourceCredentials(c *gin.Context) {
	creds := s.credentialsManager.ListGenericCredentials(models.CredentialKindGit)
	c.JSON(http.StatusOK, gin.H{"credentials": creds})
}

func (s *Server) createSourceCredential(c *gin.Context) {
	var req struct {
		Name     string `json:"name" binding:"required"`
		Username string `json:"username"`
		Token    string `json:"token" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	data := map[string]string{"token": req.Token}
	if req.Username != "" {
		data["username"] = req.Username
	}

	cred, err := s.credentialsManager.CreateGenericCredential(req.Name, models.CredentialKindGit, data)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "Credential created", "credential": cred})
}

func (s *Server) deleteSourceCredential(c *gin.Context) {
	id := c.Param("id")
	if err := s.credentialsManager.DeleteGenericCredential(id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Credential deleted", "id": id})
}
