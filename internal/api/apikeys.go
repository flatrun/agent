package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/gin-gonic/gin"
)

func (s *Server) listAPIKeys(c *gin.Context) {
	actor := auth.GetActorFromContext(c)
	if actor == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Not authenticated"})
		return
	}

	var keys []auth.APIKey
	var err error

	if actor.Role == auth.RoleAdmin {
		keys, err = s.authManager.GetAllAPIKeys()
	} else if actor.User != nil {
		keys, err = s.authManager.GetAPIKeysByUser(actor.User.ID)
	} else {
		c.JSON(http.StatusForbidden, gin.H{"error": "Cannot list API keys"})
		return
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list API keys"})
		return
	}

	response := make([]gin.H, 0, len(keys))
	for _, k := range keys {
		response = append(response, apiKeyToResponse(&k))
	}

	c.JSON(http.StatusOK, gin.H{"api_keys": response})
}

func (s *Server) getAPIKey(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid API key ID"})
		return
	}

	key, err := s.authManager.GetAPIKey(id)
	if err == auth.ErrAPIKeyNotFound {
		c.JSON(http.StatusNotFound, gin.H{"error": "API key not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get API key"})
		return
	}

	actor := auth.GetActorFromContext(c)
	if actor.Role != auth.RoleAdmin && (actor.User == nil || actor.User.ID != key.UserID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"api_key": apiKeyToResponse(key)})
}

func (s *Server) createAPIKey(c *gin.Context) {
	actor := auth.GetActorFromContext(c)
	if actor == nil || actor.User == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Not authenticated"})
		return
	}

	var req struct {
		Name        string    `json:"name" binding:"required"`
		Description string    `json:"description"`
		Role        auth.Role `json:"role"`
		Permissions []string  `json:"permissions"`
		Deployments []string  `json:"deployments"`
		ExpiresIn   int       `json:"expires_in"`
		UserID      int64     `json:"user_id"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	userID := actor.User.ID
	if req.UserID > 0 && actor.Role == auth.RoleAdmin {
		userID = req.UserID
	}

	if req.Role != "" && !req.Role.IsValid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid role"})
		return
	}

	if actor.Role != auth.RoleAdmin {
		if req.Role == auth.RoleAdmin {
			c.JSON(http.StatusForbidden, gin.H{"error": "Cannot create admin API key"})
			return
		}
	}

	var expiresAt time.Time
	if req.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(req.ExpiresIn) * time.Second)
	}

	key, plainKey, err := s.authManager.CreateAPIKey(
		userID,
		req.Name,
		req.Description,
		req.Role,
		req.Permissions,
		req.Deployments,
		expiresAt,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create API key"})
		return
	}

	response := apiKeyToResponse(key)
	response["key"] = plainKey

	c.JSON(http.StatusCreated, gin.H{
		"api_key": response,
		"message": "Save this key securely. It will not be shown again.",
	})
}

func (s *Server) deleteAPIKey(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid API key ID"})
		return
	}

	key, err := s.authManager.GetAPIKey(id)
	if err == auth.ErrAPIKeyNotFound {
		c.JSON(http.StatusNotFound, gin.H{"error": "API key not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get API key"})
		return
	}

	actor := auth.GetActorFromContext(c)
	if actor.Role != auth.RoleAdmin && (actor.User == nil || actor.User.ID != key.UserID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	if err := s.authManager.DeleteAPIKey(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete API key"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "API key deleted"})
}

func (s *Server) revokeAPIKey(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid API key ID"})
		return
	}

	key, err := s.authManager.GetAPIKey(id)
	if err == auth.ErrAPIKeyNotFound {
		c.JSON(http.StatusNotFound, gin.H{"error": "API key not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get API key"})
		return
	}

	actor := auth.GetActorFromContext(c)
	if actor.Role != auth.RoleAdmin && (actor.User == nil || actor.User.ID != key.UserID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	if err := s.authManager.DeactivateAPIKey(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to revoke API key"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "API key revoked"})
}

func apiKeyToResponse(k *auth.APIKey) gin.H {
	return gin.H{
		"id":           k.ID,
		"key_id":       k.KeyID,
		"user_id":      k.UserID,
		"name":         k.Name,
		"description":  k.Description,
		"key_prefix":   k.KeyPrefix,
		"role":         k.Role,
		"permissions":  k.Permissions,
		"deployments":  k.Deployments,
		"expires_at":   k.ExpiresAt,
		"last_used_at": k.LastUsedAt,
		"last_used_ip": k.LastUsedIP,
		"is_active":    k.IsActive,
		"created_at":   k.CreatedAt,
	}
}
