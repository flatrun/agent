package api

import (
	"net/http"
	"strconv"

	"github.com/flatrun/agent/internal/auth"
	"github.com/gin-gonic/gin"
)

func (s *Server) getUserDeployments(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	deployments, err := s.authManager.GetUserDeployments(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get user deployments"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"deployments": deploymentsToResponse(deployments)})
}

func (s *Server) assignUserDeployment(c *gin.Context) {
	idStr := c.Param("id")
	userID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	var req struct {
		DeploymentName string `json:"deployment_name" binding:"required"`
		AccessLevel    string `json:"access_level" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	if !auth.ValidAccessLevel(req.AccessLevel) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid access level. Must be read, write, or admin"})
		return
	}

	actor := auth.GetActorFromContext(c)
	if actor != nil && actor.Role != auth.RoleAdmin {
		if !actor.CanAccessDeployment(req.DeploymentName, auth.AccessLevelAdmin) {
			c.JSON(http.StatusForbidden, gin.H{"error": "No admin access to this deployment"})
			return
		}
	}

	grantedBy := int64(0)
	if actor != nil && actor.User != nil {
		grantedBy = actor.User.ID
	}

	if err := s.authManager.AssignDeployment(userID, req.DeploymentName, req.AccessLevel, grantedBy); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to assign deployment"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message":         "Deployment access granted",
		"deployment_name": req.DeploymentName,
		"access_level":    req.AccessLevel,
	})
}

func (s *Server) updateUserDeployment(c *gin.Context) {
	idStr := c.Param("id")
	userID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	deploymentName := c.Param("name")
	if deploymentName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Deployment name required"})
		return
	}

	var req struct {
		AccessLevel string `json:"access_level" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	if !auth.ValidAccessLevel(req.AccessLevel) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid access level"})
		return
	}

	actor := auth.GetActorFromContext(c)
	if actor != nil && actor.Role != auth.RoleAdmin {
		if !actor.CanAccessDeployment(deploymentName, auth.AccessLevelAdmin) {
			c.JSON(http.StatusForbidden, gin.H{"error": "No admin access to this deployment"})
			return
		}
	}

	if err := s.authManager.UpdateUserDeployment(userID, deploymentName, req.AccessLevel); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update deployment access"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":         "Deployment access updated",
		"deployment_name": deploymentName,
		"access_level":    req.AccessLevel,
	})
}

func (s *Server) removeUserDeployment(c *gin.Context) {
	idStr := c.Param("id")
	userID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	deploymentName := c.Param("name")
	if deploymentName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Deployment name required"})
		return
	}

	if !s.requireDeploymentAccess(c, deploymentName, auth.AccessLevelAdmin) {
		return
	}

	if err := s.authManager.RemoveDeploymentAccess(userID, deploymentName); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove deployment access"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Deployment access removed"})
}

func (s *Server) getDeploymentUsers(c *gin.Context) {
	deploymentName := c.Param("name")
	if deploymentName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Deployment name required"})
		return
	}

	deploymentUsers, err := s.authManager.GetDeploymentUsers(deploymentName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get deployment users"})
		return
	}

	users := make([]gin.H, 0, len(deploymentUsers))
	for _, du := range deploymentUsers {
		user, err := s.authManager.GetUser(du.UserID)
		if err != nil {
			continue
		}
		users = append(users, gin.H{
			"user_id":      du.UserID,
			"username":     user.Username,
			"email":        user.Email,
			"role":         user.Role,
			"access_level": du.AccessLevel,
			"granted_by":   du.GrantedBy,
			"created_at":   du.CreatedAt,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"deployment_name": deploymentName,
		"users":           users,
	})
}
