package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/flatrun/agent/internal/contextkeys"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	Username  string `json:"username"`
	UserID    int64  `json:"user_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	jwt.RegisteredClaims
}

type Middleware struct {
	config  *config.AuthConfig
	manager *Manager
}

func NewMiddleware(cfg *config.AuthConfig) *Middleware {
	return &Middleware{config: cfg}
}

func NewMiddlewareWithManager(cfg *config.AuthConfig, manager *Manager) *Middleware {
	return &Middleware{config: cfg, manager: manager}
}

func (m *Middleware) SetManager(manager *Manager) {
	m.manager = manager
}

func (m *Middleware) RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !m.config.Enabled {
			c.Set(contextkeys.ActorType, "anonymous")
			c.Set(contextkeys.Actor, &ActorContext{
				Type: "anonymous",
				Role: RoleAdmin,
			})
			c.Next()
			return
		}

		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Authorization header required",
			})
			c.Abort()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Invalid authorization header format",
			})
			c.Abort()
			return
		}

		scheme := strings.ToLower(parts[0])
		token := parts[1]

		switch scheme {
		case "bearer":
			if claims := m.validateJWTWithClaims(token); claims != nil {
				if err := m.setJWTContext(c, claims, token); err == nil {
					c.Next()
					return
				}
			}
			if m.handleAPIKey(c, token) {
				c.Next()
				return
			}
		case "apikey":
			if m.handleAPIKey(c, token) {
				c.Next()
				return
			}
		default:
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Unsupported authorization scheme",
			})
			c.Abort()
			return
		}

		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "Invalid or expired token",
		})
		c.Abort()
	}
}

func (m *Middleware) handleAPIKey(c *gin.Context, token string) bool {
	if m.manager != nil {
		apiKey, user, err := m.manager.ValidateAPIKey(token)
		if err == nil {
			actor, err := m.manager.BuildActorContext(user, apiKey)
			if err == nil {
				c.Set(contextkeys.ActorType, "api_key")
				c.Set(contextkeys.ActorID, fmt.Sprintf("key_%s", apiKey.KeyID))
				c.Set(contextkeys.ActorName, user.Username)
				c.Set(contextkeys.APIKeyPrefix, apiKey.KeyPrefix)
				c.Set(contextkeys.Actor, actor)

				go func() { _ = m.manager.UpdateAPIKeyLastUsed(apiKey.ID, c.ClientIP()) }()
				return true
			}
		}
	}

	if keyIndex := m.validateAPIKeyWithIndex(token); keyIndex >= 0 {
		m.setLegacyAPIKeyContext(c, token, keyIndex)
		log.Printf("Warning: Legacy API key used. Consider migrating to user-based API keys.")
		return true
	}

	return false
}

func (m *Middleware) setJWTContext(c *gin.Context, claims *Claims, token string) error {
	c.Set(contextkeys.ActorType, "jwt")
	c.Set(contextkeys.ActorID, claims.Username)
	c.Set(contextkeys.ActorName, claims.Username)

	if m.manager != nil && claims.SessionID != "" {
		session, err := m.manager.GetSessionByID(claims.SessionID)
		if err != nil || !session.RevokedAt.IsZero() {
			return fmt.Errorf("session revoked or invalid")
		}
	}

	if m.manager != nil && claims.UserID > 0 {
		user, err := m.manager.GetUser(claims.UserID)
		if err != nil {
			return err
		}

		if !user.IsActive {
			return ErrUserInactive
		}

		actor, err := m.manager.BuildActorContext(user, nil)
		if err != nil {
			return err
		}
		c.Set(contextkeys.Actor, actor)
	} else {
		c.Set(contextkeys.Actor, &ActorContext{
			Type: "jwt",
			Role: RoleAdmin,
		})
	}

	return nil
}

func (m *Middleware) setLegacyAPIKeyContext(c *gin.Context, token string, keyIndex int) {
	c.Set(contextkeys.ActorType, "api_key")
	c.Set(contextkeys.ActorID, fmt.Sprintf("key_%d", keyIndex))
	if len(token) >= 8 {
		c.Set(contextkeys.APIKeyPrefix, token[:8]+"...")
	} else {
		c.Set(contextkeys.APIKeyPrefix, token+"...")
	}

	c.Set(contextkeys.Actor, &ActorContext{
		Type: "legacy_key",
		Role: RoleAdmin,
	})
}

func (m *Middleware) validateJWTWithClaims(tokenString string) *Claims {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		return []byte(m.config.JWTSecret), nil
	})

	if err != nil {
		return nil
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		if claims.ExpiresAt != nil && claims.ExpiresAt.Time.Before(time.Now()) {
			return nil
		}
		return claims
	}

	return nil
}

func (m *Middleware) validateAPIKey(key string) bool {
	for _, validKey := range m.config.APIKeys {
		if subtle.ConstantTimeCompare([]byte(key), []byte(validKey)) == 1 {
			return true
		}
	}
	return false
}

func (m *Middleware) validateAPIKeyWithIndex(key string) int {
	for i, validKey := range m.config.APIKeys {
		if subtle.ConstantTimeCompare([]byte(key), []byte(validKey)) == 1 {
			return i
		}
	}
	return -1
}

func (m *Middleware) validateJWT(tokenString string) bool {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		return []byte(m.config.JWTSecret), nil
	})

	if err != nil {
		return false
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		if claims.ExpiresAt != nil && claims.ExpiresAt.Time.Before(time.Now()) {
			return false
		}
		return true
	}

	return false
}

func (m *Middleware) GenerateJWT(username string) (string, error) {
	claims := Claims{
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
			Issuer:    "flatrun-agent",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(m.config.JWTSecret))
}

func (m *Middleware) GenerateJWTForUser(user *User, sessionID string) (string, error) {
	claims := Claims{
		Username:  user.Username,
		UserID:    user.ID,
		SessionID: sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
			Issuer:    "flatrun-agent",
			Subject:   user.UID,
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(m.config.JWTSecret))
}

func (m *Middleware) ValidateToken(c *gin.Context) {
	if !m.config.Enabled {
		c.JSON(http.StatusOK, gin.H{
			"valid":   true,
			"message": "Authentication disabled",
		})
		return
	}

	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		c.JSON(http.StatusUnauthorized, gin.H{
			"valid": false,
			"error": "No authorization header",
		})
		return
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 {
		c.JSON(http.StatusUnauthorized, gin.H{
			"valid": false,
			"error": "Invalid header format",
		})
		return
	}

	token := parts[1]
	if m.validateJWT(token) || m.validateAPIKey(token) {
		c.JSON(http.StatusOK, gin.H{
			"valid":   true,
			"message": "Token is valid",
		})
		return
	}

	if m.manager != nil {
		if _, _, err := m.manager.ValidateAPIKey(token); err == nil {
			c.JSON(http.StatusOK, gin.H{
				"valid":   true,
				"message": "Token is valid",
			})
			return
		}
	}

	c.JSON(http.StatusUnauthorized, gin.H{
		"valid": false,
		"error": "Invalid token",
	})
}

func (m *Middleware) Login(c *gin.Context) {
	if !m.config.Enabled {
		c.JSON(http.StatusOK, gin.H{
			"token":   "",
			"message": "Authentication disabled",
		})
		return
	}

	var req struct {
		APIKey   string `json:"api_key"`
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid request body",
		})
		return
	}

	if req.Username != "" && req.Password != "" && m.manager != nil {
		user, err := m.manager.ValidateCredentials(req.Username, req.Password)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Invalid username or password",
			})
			return
		}

		sessionID, err := GenerateSessionID()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Failed to generate session",
			})
			return
		}

		token, err := m.GenerateJWTForUser(user, sessionID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Failed to generate token",
			})
			return
		}

		tokenHash := sha256.Sum256([]byte(token))
		_, _ = m.manager.CreateSession(user.ID, 0, sessionID, hex.EncodeToString(tokenHash[:]), c.ClientIP(), time.Now().Add(24*time.Hour))

		deployments, _ := m.manager.GetUserDeployments(user.ID)
		depAccess := make([]gin.H, 0, len(deployments))
		for _, d := range deployments {
			depAccess = append(depAccess, gin.H{
				"deployment_name": d.DeploymentName,
				"access_level":    d.AccessLevel,
			})
		}

		c.JSON(http.StatusOK, gin.H{
			"token":       token,
			"expires_in":  86400,
			"token_type":  "Bearer",
			"user":        userResponse(user),
			"permissions": EffectivePermissions(user, user.Role),
			"deployments": depAccess,
		})
		return
	}

	if req.APIKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "API key or username/password required",
		})
		return
	}

	if m.manager != nil {
		apiKey, user, err := m.manager.ValidateAPIKey(req.APIKey)
		if err == nil {
			sessionID, err := GenerateSessionID()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": "Failed to generate session",
				})
				return
			}

			token, err := m.GenerateJWTForUser(user, sessionID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": "Failed to generate token",
				})
				return
			}

			tokenHash := sha256.Sum256([]byte(token))
			_, _ = m.manager.CreateSession(user.ID, apiKey.ID, sessionID, hex.EncodeToString(tokenHash[:]), c.ClientIP(), time.Now().Add(24*time.Hour))

			role := user.Role
			if apiKey.Role != "" {
				role = apiKey.Role
			}

			perms := GetRolePermissions(role)
			if len(apiKey.Permissions) > 0 {
				customPerms := make([]Permission, 0, len(apiKey.Permissions))
				for _, p := range apiKey.Permissions {
					customPerms = append(customPerms, Permission(p))
				}
				perms = customPerms
			}

			deployments, _ := m.manager.GetUserDeployments(user.ID)
			depAccess := make([]gin.H, 0, len(deployments))
			for _, d := range deployments {
				depAccess = append(depAccess, gin.H{
					"deployment_name": d.DeploymentName,
					"access_level":    d.AccessLevel,
				})
			}

			c.JSON(http.StatusOK, gin.H{
				"token":       token,
				"expires_in":  86400,
				"token_type":  "Bearer",
				"user":        userResponse(user),
				"permissions": perms,
				"deployments": depAccess,
			})
			return
		}
	}

	if !m.validateAPIKey(req.APIKey) {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "Invalid API key",
		})
		return
	}

	token, err := m.GenerateJWT("api-user")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to generate token",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"token":       token,
		"expires_in":  86400,
		"token_type":  "Bearer",
		"permissions": GetRolePermissions(RoleAdmin),
	})
}

func (m *Middleware) GetAuthStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"enabled": m.config.Enabled,
	})
}

func (m *Middleware) ValidateTokenString(token string) bool {
	_, err := m.ActorForTokenString(token, "")
	return err == nil
}

func (m *Middleware) ActorForTokenString(token string, clientIP string) (*ActorContext, error) {
	if !m.config.Enabled {
		return &ActorContext{
			Type: "anonymous",
			Role: RoleAdmin,
		}, nil
	}

	if claims := m.validateJWTWithClaims(token); claims != nil {
		if m.manager != nil && claims.SessionID != "" {
			session, err := m.manager.GetSessionByID(claims.SessionID)
			if err != nil || !session.RevokedAt.IsZero() {
				return nil, fmt.Errorf("session revoked or invalid")
			}
		}

		if m.manager != nil && claims.UserID > 0 {
			user, err := m.manager.GetUser(claims.UserID)
			if err != nil {
				return nil, err
			}
			if !user.IsActive {
				return nil, ErrUserInactive
			}
			return m.manager.BuildActorContext(user, nil)
		}

		return &ActorContext{
			Type: "jwt",
			Role: RoleAdmin,
		}, nil
	}

	if m.manager != nil {
		apiKey, user, err := m.manager.ValidateAPIKey(token)
		if err == nil {
			if clientIP != "" {
				_ = m.manager.UpdateAPIKeyLastUsed(apiKey.ID, clientIP)
			}
			return m.manager.BuildActorContext(user, apiKey)
		}
	}

	if m.validateAPIKey(token) {
		log.Printf("Warning: Legacy API key used. Consider migrating to user-based API keys.")
		return &ActorContext{
			Type: "legacy_key",
			Role: RoleAdmin,
		}, nil
	}

	return nil, fmt.Errorf("invalid or expired token")
}

func (m *Middleware) IsAuthEnabled() bool {
	return m.config.Enabled
}

func (m *Middleware) RequirePermission(perms ...Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		actorVal, exists := c.Get(contextkeys.Actor)
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Not authenticated",
			})
			c.Abort()
			return
		}

		actor, ok := actorVal.(*ActorContext)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Invalid actor context",
			})
			c.Abort()
			return
		}

		for _, perm := range perms {
			if !actor.HasPermission(perm) {
				c.JSON(http.StatusForbidden, gin.H{
					"error": fmt.Sprintf("Permission denied: %s required", perm),
				})
				c.Abort()
				return
			}
		}

		c.Next()
	}
}

func (m *Middleware) RequireDeploymentAccess(level string) gin.HandlerFunc {
	return func(c *gin.Context) {
		actorVal, exists := c.Get(contextkeys.Actor)
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Not authenticated",
			})
			c.Abort()
			return
		}

		actor, ok := actorVal.(*ActorContext)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Invalid actor context",
			})
			c.Abort()
			return
		}

		deploymentName := c.Param("name")
		if deploymentName == "" {
			deploymentName = c.Param("deployment")
		}

		if deploymentName == "" {
			c.Next()
			return
		}

		if !actor.CanAccessDeployment(deploymentName, level) {
			c.JSON(http.StatusForbidden, gin.H{
				"error": "No access to this deployment",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

func GetActorFromContext(c *gin.Context) *ActorContext {
	actorVal, exists := c.Get(contextkeys.Actor)
	if !exists {
		return nil
	}
	actor, ok := actorVal.(*ActorContext)
	if !ok {
		return nil
	}
	return actor
}

func userResponse(u *User) gin.H {
	resp := gin.H{
		"id":            u.ID,
		"uid":           u.UID,
		"username":      u.Username,
		"email":         u.Email,
		"role":          u.Role,
		"is_active":     u.IsActive,
		"created_at":    u.CreatedAt,
		"last_login_at": u.LastLoginAt,
	}
	if len(u.Permissions) > 0 {
		resp["permissions"] = u.Permissions
	}
	return resp
}
