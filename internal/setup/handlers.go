package setup

import (
	"net"
	"net/http"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/version"
	"github.com/gin-gonic/gin"
)

type Handlers struct {
	manager     *Manager
	authManager *auth.Manager
}

func NewHandlers(manager *Manager, authManager *auth.Manager) *Handlers {
	return &Handlers{
		manager:     manager,
		authManager: authManager,
	}
}

func (h *Handlers) Guard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if h.manager.IsComplete() {
			c.JSON(http.StatusForbidden, gin.H{
				"error": "Setup has already been completed",
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

func (h *Handlers) GetStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"initialized": h.manager.IsComplete(),
	})
}

func (h *Handlers) GetInfo(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"instance_ip":   h.manager.GetInstanceIP(),
		"agent_version": version.Get(),
	})
}

func (h *Handlers) ValidateSystem(c *gin.Context) {
	checks := RunSystemChecks(h.manager.config.DeploymentsPath)
	c.JSON(http.StatusOK, gin.H{
		"checks": checks,
	})
}

func (h *Handlers) VerifyDNS(c *gin.Context) {
	domain := c.Query("domain")
	if domain == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "domain parameter is required"})
		return
	}

	ips, err := net.LookupHost(domain)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"valid":    false,
			"domain":   domain,
			"expected": h.manager.GetInstanceIP(),
			"actual":   []string{},
			"message":  "DNS lookup failed: " + err.Error(),
		})
		return
	}

	instanceIP := h.manager.GetInstanceIP()
	valid := false
	for _, ip := range ips {
		if ip == instanceIP {
			valid = true
			break
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"valid":    valid,
		"domain":   domain,
		"expected": instanceIP,
		"actual":   ips,
	})
}

func (h *Handlers) ConfigureSettings(c *gin.Context) {
	var req struct {
		Domain      string   `json:"domain"`
		AutoSSL     *bool    `json:"auto_ssl"`
		CORSOrigins []string `json:"cors_origins"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	cfg := h.manager.config
	if req.Domain != "" {
		cfg.Domain.DefaultDomain = req.Domain
	}
	if req.AutoSSL != nil {
		cfg.Domain.AutoSSL = *req.AutoSSL
	}
	if len(req.CORSOrigins) > 0 {
		originMap := make(map[string]bool)
		for _, e := range cfg.API.AllowedOrigins {
			originMap[e] = true
		}
		for _, origin := range req.CORSOrigins {
			if !originMap[origin] {
				cfg.API.AllowedOrigins = append(cfg.API.AllowedOrigins, origin)
				originMap[origin] = true
			}
		}
	}

	if err := config.Save(cfg, h.manager.configPath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save configuration: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":  "Settings configured",
		"domain":   cfg.Domain.DefaultDomain,
		"auto_ssl": cfg.Domain.AutoSSL,
	})
}

func (h *Handlers) ConfigureAuthentication(c *gin.Context) {
	var req struct {
		AuthMethod string `json:"auth_method"`
		Username   string `json:"username"`
		Password   string `json:"password"`
		Email      string `json:"email"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.AuthMethod == "" {
		req.AuthMethod = "both"
	}
	if req.AuthMethod != "password" && req.AuthMethod != "apikey" && req.AuthMethod != "both" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_method must be 'password', 'apikey', or 'both'"})
		return
	}

	if h.authManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Auth manager not available"})
		return
	}

	result := gin.H{
		"auth_method": req.AuthMethod,
	}

	var userID int64

	if req.AuthMethod == "password" || req.AuthMethod == "both" {
		if req.Username == "" || req.Password == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "username and password are required for password authentication"})
			return
		}
		if len(req.Password) < 8 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Password must be at least 8 characters"})
			return
		}

		user, err := h.authManager.CreateUser(req.Username, req.Email, req.Password, auth.RoleAdmin, nil)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create user: " + err.Error()})
			return
		}
		userID = user.ID
		result["username"] = user.Username
		result["user_uid"] = user.UID
	}

	if req.AuthMethod == "apikey" || req.AuthMethod == "both" {
		if userID == 0 {
			username := req.Username
			if username == "" {
				username = "system"
			}
			sysUser, err := h.authManager.CreateUser(username, "", "apikey-only-no-password-login", auth.RoleAdmin, nil)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create system user: " + err.Error()})
				return
			}
			userID = sysUser.ID
		}

		apiKey, plainKey, err := h.authManager.CreateAPIKey(
			userID,
			"Setup API Key",
			"Generated during initial setup",
			auth.RoleAdmin,
			nil,
			nil,
			time.Time{},
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create API key: " + err.Error()})
			return
		}
		result["api_key"] = plainKey
		result["api_key_id"] = apiKey.KeyID
	}

	c.JSON(http.StatusOK, result)
}

func (h *Handlers) Complete(c *gin.Context) {
	if err := h.manager.MarkComplete(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to mark setup complete: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":      "Setup completed successfully",
		"completed_at": time.Now().UTC().Format(time.RFC3339),
	})
}
