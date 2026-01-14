package auth

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

const (
	ContextKeyActorType    = "audit_actor_type"
	ContextKeyActorID      = "audit_actor_id"
	ContextKeyActorName    = "audit_actor_name"
	ContextKeyAPIKeyPrefix = "audit_api_key_prefix"
)

type Claims struct {
	Username string `json:"username"`
	jwt.RegisteredClaims
}

type Middleware struct {
	config *config.AuthConfig
}

func NewMiddleware(cfg *config.AuthConfig) *Middleware {
	return &Middleware{config: cfg}
}

func (m *Middleware) RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !m.config.Enabled {
			c.Set(ContextKeyActorType, "anonymous")
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
				c.Set(ContextKeyActorType, "jwt")
				c.Set(ContextKeyActorID, claims.Username)
				c.Set(ContextKeyActorName, claims.Username)
				c.Next()
				return
			}
			if keyIndex := m.validateAPIKeyWithIndex(token); keyIndex >= 0 {
				m.setAPIKeyContext(c, token, keyIndex)
				c.Next()
				return
			}
		case "apikey":
			if keyIndex := m.validateAPIKeyWithIndex(token); keyIndex >= 0 {
				m.setAPIKeyContext(c, token, keyIndex)
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

func (m *Middleware) setAPIKeyContext(c *gin.Context, token string, keyIndex int) {
	c.Set(ContextKeyActorType, "api_key")
	c.Set(ContextKeyActorID, fmt.Sprintf("key_%d", keyIndex))
	if len(token) >= 8 {
		c.Set(ContextKeyAPIKeyPrefix, token[:8]+"...")
	} else {
		c.Set(ContextKeyAPIKeyPrefix, token+"...")
	}
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
		APIKey string `json:"api_key"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid request body",
		})
		return
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
		"token":      token,
		"expires_in": 86400,
		"token_type": "Bearer",
	})
}

func (m *Middleware) GetAuthStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"enabled": m.config.Enabled,
	})
}

func (m *Middleware) ValidateTokenString(token string) bool {
	if !m.config.Enabled {
		return true
	}
	return m.validateJWT(token) || m.validateAPIKey(token)
}

func (m *Middleware) IsAuthEnabled() bool {
	return m.config.Enabled
}
