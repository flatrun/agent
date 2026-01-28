package api

import (
	"net/http"
	"strconv"

	"github.com/flatrun/agent/internal/auth"
	"github.com/gin-gonic/gin"
)

func (s *Server) listUsers(c *gin.Context) {
	users, err := s.authManager.GetUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list users"})
		return
	}

	response := make([]gin.H, 0, len(users))
	for _, u := range users {
		resp := userToResponse(&u)
		deps, _ := s.authManager.GetUserDeployments(u.ID)
		resp["deployment_count"] = len(deps)
		response = append(response, resp)
	}

	c.JSON(http.StatusOK, gin.H{"users": response})
}

func (s *Server) getUser(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	user, err := s.authManager.GetUser(id)
	if err == auth.ErrUserNotFound {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get user"})
		return
	}

	deployments, _ := s.authManager.GetUserDeployments(user.ID)

	c.JSON(http.StatusOK, gin.H{
		"user":        userToResponse(user),
		"deployments": deploymentsToResponse(deployments),
	})
}

func (s *Server) createUser(c *gin.Context) {
	var req struct {
		Username    string    `json:"username" binding:"required"`
		Email       string    `json:"email"`
		Password    string    `json:"password" binding:"required"`
		Role        auth.Role `json:"role" binding:"required"`
		Permissions []string  `json:"permissions"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	if !req.Role.IsValid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid role. Must be admin, operator, or viewer"})
		return
	}

	actor := auth.GetActorFromContext(c)
	if actor != nil && actor.Role != auth.RoleAdmin && req.Role == auth.RoleAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "Only admins can create admin users"})
		return
	}

	user, err := s.authManager.CreateUser(req.Username, req.Email, req.Password, req.Role, req.Permissions)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create user: " + err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"user": userToResponse(user)})
}

func (s *Server) updateUser(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	user, err := s.authManager.GetUser(id)
	if err == auth.ErrUserNotFound {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get user"})
		return
	}

	var req struct {
		Username    string    `json:"username"`
		Email       string    `json:"email"`
		Role        auth.Role `json:"role"`
		Permissions *[]string `json:"permissions"`
		IsActive    *bool     `json:"is_active"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	if req.Username != "" {
		user.Username = req.Username
	}
	if req.Email != "" {
		user.Email = req.Email
	}
	if req.Role != "" {
		if !req.Role.IsValid() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid role"})
			return
		}
		actor := auth.GetActorFromContext(c)
		if req.Role == auth.RoleAdmin && actor != nil && actor.Role != auth.RoleAdmin {
			c.JSON(http.StatusForbidden, gin.H{"error": "Only admins can assign the admin role"})
			return
		}
		user.Role = req.Role
	}
	if req.Permissions != nil {
		user.Permissions = *req.Permissions
	}
	if req.IsActive != nil {
		user.IsActive = *req.IsActive
	}

	if err := s.authManager.UpdateUser(user); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update user"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"user": userToResponse(user)})
}

func (s *Server) deleteUser(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	actor := auth.GetActorFromContext(c)
	if actor != nil && actor.UserID == id {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete your own account"})
		return
	}

	if err := s.authManager.DeleteUser(id, actor.UserID); err != nil {
		if err == auth.ErrCannotDeleteSelf {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete user"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "User deleted"})
}

func (s *Server) getCurrentUser(c *gin.Context) {
	actor := auth.GetActorFromContext(c)
	if actor == nil || actor.User == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Not authenticated"})
		return
	}

	deployments, _ := s.authManager.GetUserDeployments(actor.User.ID)
	permissions := auth.EffectivePermissions(actor.User, actor.Role)

	c.JSON(http.StatusOK, gin.H{
		"user":        userToResponse(actor.User),
		"permissions": permissions,
		"deployments": deploymentsToResponse(deployments),
	})
}

func (s *Server) updateCurrentUser(c *gin.Context) {
	actor := auth.GetActorFromContext(c)
	if actor == nil || actor.User == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Not authenticated"})
		return
	}

	var req struct {
		Email string `json:"email"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	user := actor.User
	if req.Email != "" {
		user.Email = req.Email
	}

	if err := s.authManager.UpdateUser(user); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update profile"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"user": userToResponse(user)})
}

func (s *Server) updateCurrentUserPassword(c *gin.Context) {
	actor := auth.GetActorFromContext(c)
	if actor == nil || actor.User == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Not authenticated"})
		return
	}

	var req struct {
		CurrentPassword string `json:"current_password" binding:"required"`
		NewPassword     string `json:"new_password" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	if !auth.VerifyPassword(req.CurrentPassword, actor.User.PasswordHash) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Current password is incorrect"})
		return
	}

	if err := s.authManager.UpdatePassword(actor.User.ID, req.NewPassword); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update password"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Password updated"})
}

func userToResponse(u *auth.User) gin.H {
	resp := gin.H{
		"id":            u.ID,
		"uid":           u.UID,
		"username":      u.Username,
		"email":         u.Email,
		"role":          u.Role,
		"is_active":     u.IsActive,
		"created_at":    u.CreatedAt,
		"updated_at":    u.UpdatedAt,
		"last_login_at": u.LastLoginAt,
	}
	if len(u.Permissions) > 0 {
		resp["permissions"] = u.Permissions
	}
	return resp
}

func deploymentsToResponse(deployments []auth.UserDeployment) []gin.H {
	result := make([]gin.H, 0, len(deployments))
	for _, d := range deployments {
		result = append(result, gin.H{
			"deployment_name": d.DeploymentName,
			"access_level":    d.AccessLevel,
			"granted_by":      d.GrantedBy,
			"created_at":      d.CreatedAt,
		})
	}
	return result
}
