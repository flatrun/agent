package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/system"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/version"
	"github.com/gin-gonic/gin"
)

type setupState struct {
	Initialized bool   `json:"initialized"`
	CompletedAt string `json:"completed_at,omitempty"`
}

func (s *Server) setupStatePath() string {
	return filepath.Join(s.config.DeploymentsPath, ".flatrun", "setup.json")
}

func isSetupCompleteCheck(deploymentsPath string) bool {
	data, err := os.ReadFile(filepath.Join(deploymentsPath, ".flatrun", "setup.json"))
	if err != nil {
		return false
	}
	var state setupState
	if err := json.Unmarshal(data, &state); err != nil {
		return false
	}
	return state.Initialized
}

func (s *Server) isSetupComplete() bool {
	return isSetupCompleteCheck(s.config.DeploymentsPath)
}

func (s *Server) markSetupComplete() error {
	state := setupState{
		Initialized: true,
		CompletedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.setupStatePath())
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(s.setupStatePath(), data, 0644)
}

func (s *Server) setupGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.isSetupComplete() {
			c.JSON(http.StatusForbidden, gin.H{
				"error": "Setup has already been completed",
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

func (s *Server) getSetupStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"initialized": s.isSetupComplete(),
	})
}

func (s *Server) getSetupInfo(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"instance_ip":   s.getInstanceIP(),
		"agent_version": version.Get(),
	})
}

func (s *Server) validateSystem(c *gin.Context) {
	checks := []gin.H{
		checkDocker(),
		checkDockerCompose(),
		checkDiskSpace(s.config.DeploymentsPath),
		checkMemory(),
		checkPort(80, "FlatRun welcome page"),
		checkPort(443, "FlatRun"),
	}

	c.JSON(http.StatusOK, gin.H{
		"checks": checks,
	})
}

func checkDocker() gin.H {
	check := gin.H{"name": "Docker", "required": true}
	if _, err := exec.LookPath("docker"); err != nil {
		check["status"] = "fail"
		check["message"] = "Docker is not installed"
		return check
	}
	out, err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").Output()
	if err != nil {
		check["status"] = "fail"
		check["message"] = "Docker is installed but not running"
	} else {
		check["status"] = "pass"
		check["message"] = fmt.Sprintf("Docker %s", strings.TrimSpace(string(out)))
	}
	return check
}

func checkDockerCompose() gin.H {
	check := gin.H{"name": "Docker Compose", "required": true}
	out, err := exec.Command("docker", "compose", "version", "--short").Output()
	if err != nil {
		check["status"] = "fail"
		check["message"] = "Docker Compose plugin not found"
	} else {
		check["status"] = "pass"
		check["message"] = fmt.Sprintf("Docker Compose %s", strings.TrimSpace(string(out)))
	}
	return check
}

func checkDiskSpace(path string) gin.H {
	check := gin.H{"name": "Disk Space", "required": true}
	diskFree := getDiskFreeGB(path)
	if diskFree < 1 {
		check["status"] = "fail"
		check["message"] = "Less than 1 GB free disk space"
	} else if diskFree < 5 {
		check["status"] = "warn"
		check["message"] = fmt.Sprintf("%.1f GB free (5 GB+ recommended)", diskFree)
	} else {
		check["status"] = "pass"
		check["message"] = fmt.Sprintf("%.1f GB free", diskFree)
	}
	return check
}

func checkMemory() gin.H {
	check := gin.H{"name": "Memory", "required": false}
	totalMB := getHostMemoryMB()
	if totalMB < 512 {
		check["status"] = "warn"
		check["message"] = fmt.Sprintf("%d MB total (512 MB+ recommended)", totalMB)
	} else {
		check["status"] = "pass"
		check["message"] = fmt.Sprintf("%d MB total", totalMB)
	}
	return check
}

func checkPort(port int, inUseBy string) gin.H {
	check := gin.H{
		"name":     fmt.Sprintf("Port %d", port),
		"required": false,
		"status":   "pass",
	}
	if isPortAvailable(port) {
		check["message"] = fmt.Sprintf("Port %d is available", port)
	} else {
		check["message"] = fmt.Sprintf("Port %d is in use by %s", port, inUseBy)
	}
	return check
}

func (s *Server) verifyDNS(c *gin.Context) {
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
			"expected": s.getInstanceIP(),
			"actual":   []string{},
			"message":  "DNS lookup failed: " + err.Error(),
		})
		return
	}

	instanceIP := s.getInstanceIP()
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

func (s *Server) configureSettings(c *gin.Context) {
	var req struct {
		Domain      string   `json:"domain"`
		AutoSSL     *bool    `json:"auto_ssl"`
		CORSOrigins []string `json:"cors_origins"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Domain != "" {
		s.config.Domain.DefaultDomain = req.Domain
	}
	if req.AutoSSL != nil {
		s.config.Domain.AutoSSL = *req.AutoSSL
	}
	if len(req.CORSOrigins) > 0 {
		originMap := make(map[string]bool)
		for _, e := range s.config.API.AllowedOrigins {
			originMap[e] = true
		}
		for _, origin := range req.CORSOrigins {
			if !originMap[origin] {
				s.config.API.AllowedOrigins = append(s.config.API.AllowedOrigins, origin)
				originMap[origin] = true
			}
		}
	}

	if err := config.Save(s.config, s.configPath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save configuration: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":  "Settings configured",
		"domain":   s.config.Domain.DefaultDomain,
		"auto_ssl": s.config.Domain.AutoSSL,
	})
}

func (s *Server) configureAuthentication(c *gin.Context) {
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

	if s.authManager == nil {
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

		user, err := s.authManager.CreateUser(req.Username, req.Email, req.Password, auth.RoleAdmin, nil)
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
			sysUser, err := s.authManager.CreateUser(username, "", "apikey-only-no-password-login", auth.RoleAdmin, nil)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create system user: " + err.Error()})
				return
			}
			userID = sysUser.ID
		}

		apiKey, plainKey, err := s.authManager.CreateAPIKey(
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

func (s *Server) completeSetup(c *gin.Context) {
	if err := s.markSetupComplete(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to mark setup complete: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":      "Setup completed successfully",
		"completed_at": time.Now().UTC().Format(time.RFC3339),
	})
}


func (s *Server) getInstanceIP() string {
	if s.cachedIP != "" {
		return s.cachedIP
	}

	ip := resolvePublicIP()
	s.cachedIP = ip
	return ip
}

func resolvePublicIP() string {
	if ip, err := system.GetPublicIP("4"); err == nil {
		return ip
	}

	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}

	return "127.0.0.1"
}

func getHostMemoryMB() uint64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return 0
			}
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0
			}
			return kb / 1024
		}
	}
	return 0
}

func isPortAvailable(port int) bool {
	portStr := fmt.Sprintf("%d", port)
	for _, host := range []string{"0.0.0.0", "127.0.0.1", "::1"} {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, portStr))
		if err != nil {
			return false
		}
		ln.Close()
	}
	return true
}

func getDiskFreeGB(path string) float64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return -1
	}
	return float64(stat.Bavail*uint64(stat.Bsize)) / (1024 * 1024 * 1024)
}
