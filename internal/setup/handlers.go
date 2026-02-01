package setup

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flatrun/agent/internal/auth"
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

func (h *Handlers) GetStatus(c *gin.Context) {
	status, err := h.manager.GetStatus()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, status)
}

func (h *Handlers) Initialize(c *gin.Context) {
	if h.manager.IsInitialized() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Setup already completed"})
		return
	}

	var req struct {
		Mode string `json:"mode" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	mode := DeploymentMode(req.Mode)
	if mode != ModeFull && mode != ModeAgentOnly {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid mode. Must be 'full' or 'agent-only'"})
		return
	}

	resp, err := h.manager.Initialize(mode)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, resp)
}

func (h *Handlers) RunValidation(c *gin.Context) {
	if h.manager.IsInitialized() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Setup already completed"})
		return
	}

	checks := h.manager.RunValidation()

	allPassed := true
	for _, check := range checks {
		if check.Required && check.Status == StatusFail {
			allPassed = false
			break
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"checks":     checks,
		"all_passed": allPassed,
	})
}

func (h *Handlers) ConfigureDomain(c *gin.Context) {
	if h.manager.IsInitialized() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Setup already completed"})
		return
	}

	var req struct {
		Domain  string `json:"domain" binding:"required"`
		AutoSSL bool   `json:"auto_ssl"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	domain := strings.TrimSpace(req.Domain)
	domain = strings.TrimPrefix(domain, "http://")
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimSuffix(domain, "/")

	if domain == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Domain is required"})
		return
	}

	if err := h.manager.ConfigureDomain(domain, req.AutoSSL); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"domain":   domain,
		"auto_ssl": req.AutoSSL,
		"message":  "Domain configured successfully",
	})
}

func (h *Handlers) VerifyDNS(c *gin.Context) {
	domain := c.Query("domain")
	if domain == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Domain parameter is required"})
		return
	}

	domain = strings.TrimSpace(domain)
	domain = strings.TrimPrefix(domain, "http://")
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimSuffix(domain, "/")

	result, err := h.manager.VerifyDNS(domain)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, result)
}

func (h *Handlers) ConfigureCORS(c *gin.Context) {
	if h.manager.IsInitialized() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Setup already completed"})
		return
	}

	var req struct {
		UIOrigin string `json:"ui_origin" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	origin := strings.TrimSpace(req.UIOrigin)
	origin = strings.TrimSuffix(origin, "/")

	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid URL format. Must include scheme (http/https)"})
		return
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "URL scheme must be http or https"})
		return
	}

	if err := h.manager.ConfigureCORS(origin); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ui_origin": origin,
		"message":   "CORS configuration saved",
	})
}

func (h *Handlers) CreateUser(c *gin.Context) {
	if h.manager.IsInitialized() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Setup already completed"})
		return
	}

	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Authentication module not enabled"})
		return
	}

	var req struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
		Email    string `json:"email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if len(req.Username) < 3 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Username must be at least 3 characters"})
		return
	}

	if len(req.Password) < 8 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Password must be at least 8 characters"})
		return
	}

	user, err := h.authManager.CreateUser(req.Username, req.Email, req.Password, auth.RoleAdmin, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	apiKey, plainKey, err := h.authManager.CreateAPIKey(
		user.ID,
		"Setup API Key",
		"Auto-generated during initial setup",
		auth.RoleAdmin,
		nil,
		nil,
		time.Time{},
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "User created but failed to generate API key: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"user_id":    user.ID,
		"username":   user.Username,
		"api_key":    plainKey,
		"api_key_id": apiKey.KeyID,
		"message":    "User and API key created successfully",
	})
}

func (h *Handlers) Complete(c *gin.Context) {
	if h.manager.IsInitialized() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Setup already completed"})
		return
	}

	if err := h.manager.Complete(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":     "Setup completed successfully",
		"initialized": true,
	})
}

func (h *Handlers) InstallUI(c *gin.Context) {
	if h.manager.IsInitialized() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Setup already completed"})
		return
	}

	if err := h.manager.InstallUI(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "UI installed successfully",
	})
}

func (h *Handlers) RequireSetupIncomplete() gin.HandlerFunc {
	return func(c *gin.Context) {
		if h.manager.IsInitialized() {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "Setup has been completed. These endpoints are no longer available.",
			})
			return
		}
		c.Next()
	}
}
