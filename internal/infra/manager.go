package infra

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
	"github.com/flatrun/agent/templates"
)

type Manager struct {
	config *config.Config
	mu     sync.RWMutex
}

func NewManager(cfg *config.Config) *Manager {
	return &Manager{
		config: cfg,
	}
}

func (m *Manager) ListServices() ([]models.InfraService, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var services []models.InfraService

	if m.config.Nginx.Enabled || m.config.Nginx.ContainerName != "" {
		nginx := m.getNginxService()
		services = append(services, nginx)
	}

	if m.config.Infrastructure.Database.Enabled {
		db := m.getDatabaseService()
		services = append(services, db)
	}

	if m.config.Infrastructure.Redis.Enabled {
		redis := m.getRedisService()
		services = append(services, redis)
	}

	return services, nil
}

func (m *Manager) GetService(name string) (*models.InfraService, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	switch name {
	case models.InfraTypeNginx, m.config.Nginx.ContainerName:
		svc := m.getNginxService()
		return &svc, nil
	case models.InfraTypeDatabase, m.config.Infrastructure.Database.Container:
		svc := m.getDatabaseService()
		return &svc, nil
	case models.InfraTypeRedis, m.config.Infrastructure.Redis.Container:
		svc := m.getRedisService()
		return &svc, nil
	default:
		return nil, fmt.Errorf("unknown infrastructure service: %s", name)
	}
}

func (m *Manager) StartService(name string) error {
	containerName := m.resolveContainerName(name)
	if containerName == "" {
		return fmt.Errorf("unknown service: %s", name)
	}

	cmd := exec.Command("docker", "start", containerName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to start %s: %s - %w", name, string(output), err)
	}
	return nil
}

func (m *Manager) StopService(name string) error {
	containerName := m.resolveContainerName(name)
	if containerName == "" {
		return fmt.Errorf("unknown service: %s", name)
	}

	cmd := exec.Command("docker", "stop", containerName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to stop %s: %s - %w", name, string(output), err)
	}
	return nil
}

func (m *Manager) RestartService(name string) error {
	containerName := m.resolveContainerName(name)
	if containerName == "" {
		return fmt.Errorf("unknown service: %s", name)
	}

	cmd := exec.Command("docker", "restart", containerName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to restart %s: %s - %w", name, string(output), err)
	}
	return nil
}

func (m *Manager) GetServiceLogs(name string, tail int) (string, error) {
	containerName := m.resolveContainerName(name)
	if containerName == "" {
		return "", fmt.Errorf("unknown service: %s", name)
	}

	args := []string{"logs"}
	if tail > 0 {
		args = append(args, "--tail", fmt.Sprintf("%d", tail))
	}
	args = append(args, containerName)

	cmd := exec.Command("docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get logs for %s: %w", name, err)
	}
	return string(output), nil
}

func (m *Manager) resolveContainerName(name string) string {
	switch name {
	case models.InfraTypeNginx:
		return m.config.Nginx.ContainerName
	case models.InfraTypeDatabase:
		return m.config.Infrastructure.Database.Container
	case models.InfraTypeRedis:
		return m.config.Infrastructure.Redis.Container
	default:
		return name
	}
}

func (m *Manager) getNginxService() models.InfraService {
	svc := models.InfraService{
		Name:     models.InfraTypeNginx,
		Type:     models.InfraTypeNginx,
		Image:    m.config.Nginx.Image,
		Managed:  !m.config.Nginx.External,
		External: m.config.Nginx.External,
		Config: map[string]any{
			"container_name": m.config.Nginx.ContainerName,
			"config_path":    m.config.Nginx.ConfigPath,
			"image":          m.config.Nginx.Image,
		},
	}

	if m.config.Nginx.External {
		svc.Status = models.InfraStatusExternal
	} else {
		svc.Status, svc.ContainerID, svc.Health, svc.CreatedAt = m.getContainerStatus(m.config.Nginx.ContainerName)
	}

	return svc
}

func (m *Manager) getDatabaseService() models.InfraService {
	dbType := m.config.Infrastructure.Database.Type
	if dbType == "" {
		dbType = "mysql"
	}

	svc := models.InfraService{
		Name:    models.InfraTypeDatabase,
		Type:    models.InfraTypeDatabase,
		Managed: m.config.Infrastructure.Database.Container != "",
		Config: map[string]any{
			"type":      dbType,
			"container": m.config.Infrastructure.Database.Container,
			"host":      m.config.Infrastructure.Database.Host,
			"port":      m.config.Infrastructure.Database.Port,
		},
	}

	if m.config.Infrastructure.Database.Container == "" {
		svc.External = true
		svc.Status = models.InfraStatusExternal
	} else {
		svc.Status, svc.ContainerID, svc.Health, svc.CreatedAt = m.getContainerStatus(m.config.Infrastructure.Database.Container)
	}

	return svc
}

func (m *Manager) getRedisService() models.InfraService {
	svc := models.InfraService{
		Name:    models.InfraTypeRedis,
		Type:    models.InfraTypeRedis,
		Managed: m.config.Infrastructure.Redis.Container != "",
		Config: map[string]any{
			"container": m.config.Infrastructure.Redis.Container,
			"host":      m.config.Infrastructure.Redis.Host,
			"port":      m.config.Infrastructure.Redis.Port,
		},
	}

	if m.config.Infrastructure.Redis.Container == "" {
		svc.External = true
		svc.Status = models.InfraStatusExternal
	} else {
		svc.Status, svc.ContainerID, svc.Health, svc.CreatedAt = m.getContainerStatus(m.config.Infrastructure.Redis.Container)
	}

	return svc
}

func (m *Manager) getContainerStatus(containerName string) (status, containerID, health string, createdAt time.Time) {
	if containerName == "" {
		return models.InfraStatusUnknown, "", "", time.Time{}
	}

	cmd := exec.Command("docker", "inspect", "--format", "{{json .}}", containerName)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return models.InfraStatusStopped, "", "", time.Time{}
	}

	var container struct {
		ID    string `json:"Id"`
		State struct {
			Status  string `json:"Status"`
			Running bool   `json:"Running"`
			Health  *struct {
				Status string `json:"Status"`
			} `json:"Health,omitempty"`
		} `json:"State"`
		Created string `json:"Created"`
	}

	if err := json.Unmarshal(stdout.Bytes(), &container); err != nil {
		return models.InfraStatusUnknown, "", "", time.Time{}
	}

	containerID = container.ID[:12]

	if container.State.Running {
		status = models.InfraStatusRunning
	} else {
		status = models.InfraStatusStopped
	}

	if container.State.Health != nil {
		health = container.State.Health.Status
	}

	if created, err := time.Parse(time.RFC3339Nano, container.Created); err == nil {
		createdAt = created
	}

	return status, containerID, health, createdAt
}

func (m *Manager) UpdateConfig(cfg *config.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config = cfg
}

type InfraStats struct {
	TotalServices int `json:"total_services"`
	Running       int `json:"running"`
	Stopped       int `json:"stopped"`
	External      int `json:"external"`
}

func (m *Manager) GetStats() (*InfraStats, error) {
	services, err := m.ListServices()
	if err != nil {
		return nil, err
	}

	stats := &InfraStats{
		TotalServices: len(services),
	}

	for _, svc := range services {
		switch {
		case svc.External:
			stats.External++
		case strings.EqualFold(svc.Status, models.InfraStatusRunning):
			stats.Running++
		default:
			stats.Stopped++
		}
	}

	return stats, nil
}

func (m *Manager) SetNginxRealtimeCapture(enabled bool) error {
	_, err := m.SetNginxRealtimeCaptureWithStatus(enabled)
	return err
}

// SetNginxRealtimeCaptureWithStatus enables/disables realtime capture and returns detailed status
func (m *Manager) SetNginxRealtimeCaptureWithStatus(enabled bool) (map[string]interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := map[string]interface{}{
		"volumes_modified":    false,
		"nginx_conf_written":  false,
		"nginx_conf_deleted":  false,
		"lua_files_written":   false,
		"lua_dir_removed":     false,
		"conf_files_written":  false,
		"container_recreated": false,
		"nginx_reloaded":      false,
		"errors":              []string{},
	}

	var errors []string

	nginxDir := m.getNginxDir()
	if nginxDir == "" {
		return result, fmt.Errorf("nginx config path not configured")
	}
	result["nginx_dir"] = nginxDir

	if err := os.MkdirAll(nginxDir, 0755); err != nil {
		return result, fmt.Errorf("failed to create nginx directory: %w", err)
	}

	luaDir := filepath.Join(nginxDir, "lua")
	confPath := filepath.Join(nginxDir, "nginx.conf")

	// Manage volume mounts in compose file
	var volumesModified bool
	var volumeErr error
	if enabled {
		volumesModified, volumeErr = m.addSecurityVolumeMountsInternal()
	} else {
		volumesModified, volumeErr = m.removeSecurityVolumeMountsInternal()
	}
	if volumeErr != nil {
		errors = append(errors, fmt.Sprintf("failed to modify volume mounts: %v", volumeErr))
	}
	result["volumes_modified"] = volumesModified

	if enabled {
		// Write nginx.conf with Lua support
		nginxConf, err := templates.GetNginxConfig(true)
		if err != nil {
			errors = append(errors, fmt.Sprintf("failed to get nginx lua config template: %v", err))
		} else {
			if err := os.WriteFile(confPath, nginxConf, 0644); err != nil {
				errors = append(errors, fmt.Sprintf("failed to write nginx.conf: %v", err))
			} else {
				result["nginx_conf_written"] = true
			}
		}

		// Create lua directory and write security.lua
		if err := os.MkdirAll(luaDir, 0755); err != nil {
			errors = append(errors, fmt.Sprintf("failed to create lua directory: %v", err))
		} else {
			securityLua, err := templates.GetNginxSecurityLua()
			if err != nil {
				errors = append(errors, fmt.Sprintf("failed to get security.lua template: %v", err))
			} else {
				luaPath := filepath.Join(luaDir, "security.lua")
				if err := os.WriteFile(luaPath, securityLua, 0644); err != nil {
					errors = append(errors, fmt.Sprintf("failed to write security.lua: %v", err))
				} else {
					result["lua_files_written"] = true
				}
			}
		}

		// Ensure conf.d directory and security config files exist
		confDir := filepath.Join(nginxDir, "conf.d")
		if err := os.MkdirAll(confDir, 0755); err != nil {
			errors = append(errors, fmt.Sprintf("failed to create conf.d directory: %v", err))
		} else {
			blockedIPsPath := filepath.Join(confDir, "blocked_ips.conf")
			if _, err := os.Stat(blockedIPsPath); os.IsNotExist(err) {
				content := "# Auto-generated by FlatRun Security\n# No blocked IPs\n"
				if err := os.WriteFile(blockedIPsPath, []byte(content), 0644); err != nil {
					errors = append(errors, fmt.Sprintf("failed to create blocked_ips.conf: %v", err))
				}
			}

			rateLimitsPath := filepath.Join(confDir, "rate_limits.conf")
			if _, err := os.Stat(rateLimitsPath); os.IsNotExist(err) {
				content := "# Auto-generated by FlatRun Security\n# No rate limit zones defined\n"
				if err := os.WriteFile(rateLimitsPath, []byte(content), 0644); err != nil {
					errors = append(errors, fmt.Sprintf("failed to create rate_limits.conf: %v", err))
				}
			}
			result["conf_files_written"] = true
		}
	} else {
		// Delete nginx.conf - container will use default from image
		if _, err := os.Stat(confPath); err == nil {
			if err := os.Remove(confPath); err != nil {
				errors = append(errors, fmt.Sprintf("failed to delete nginx.conf: %v", err))
			} else {
				result["nginx_conf_deleted"] = true
			}
		} else {
			result["nginx_conf_deleted"] = true // Already doesn't exist
		}

		// Remove lua directory
		if _, err := os.Stat(luaDir); err == nil {
			if err := os.RemoveAll(luaDir); err != nil {
				errors = append(errors, fmt.Sprintf("failed to remove lua directory: %v", err))
			} else {
				result["lua_dir_removed"] = true
			}
		} else {
			result["lua_dir_removed"] = true // Already doesn't exist
		}
	}

	// Recreate or reload nginx container
	if m.config.Nginx.ContainerName != "" {
		if volumesModified {
			if err := m.recreateNginxContainer(); err != nil {
				errors = append(errors, fmt.Sprintf("failed to recreate nginx container: %v", err))
			} else {
				result["container_recreated"] = true
			}
		} else {
			if err := m.reloadNginx(); err != nil {
				errors = append(errors, fmt.Sprintf("failed to reload nginx: %v", err))
			} else {
				result["nginx_reloaded"] = true
			}
		}
	}

	result["errors"] = errors

	if len(errors) > 0 {
		return result, fmt.Errorf("completed with %d error(s)", len(errors))
	}

	return result, nil
}

// IsNginxRunning checks if the nginx container is running
func (m *Manager) IsNginxRunning() bool {
	if m.config.Nginx.ContainerName == "" {
		return false
	}

	cmd := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", m.config.Nginx.ContainerName)
	output, err := cmd.Output()
	if err != nil {
		return false
	}

	return strings.TrimSpace(string(output)) == "true"
}

func (m *Manager) reloadNginx() error {
	reloadCmd := m.config.Nginx.ReloadCommand
	if reloadCmd == "" {
		reloadCmd = "nginx -s reload"
	}

	cmd := exec.Command("docker", "exec", m.config.Nginx.ContainerName, "sh", "-c", reloadCmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", string(output), err)
	}
	return nil
}

func (m *Manager) getNginxDir() string {
	configPath := m.config.Nginx.ConfigPath
	if configPath == "" {
		return filepath.Join(m.config.DeploymentsPath, "nginx")
	}
	return filepath.Dir(configPath)
}

// SecurityHealthCheck represents the result of a security setup health check
type SecurityHealthCheck struct {
	Status          string                 `json:"status"`
	Checks          map[string]bool        `json:"checks"`
	Issues          []string               `json:"issues"`
	Recommendations []string               `json:"recommendations"`
	Details         map[string]interface{} `json:"details,omitempty"`
}

// CheckSecurityHealth verifies the security setup is correctly configured
func (m *Manager) CheckSecurityHealth() *SecurityHealthCheck {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := &SecurityHealthCheck{
		Status:          "healthy",
		Checks:          make(map[string]bool),
		Issues:          []string{},
		Recommendations: []string{},
		Details:         make(map[string]interface{}),
	}

	nginxDir := m.getNginxDir()
	result.Details["nginx_dir"] = nginxDir
	result.Details["nginx_container"] = m.config.Nginx.ContainerName

	// Check 1: security.lua exists
	luaPath := filepath.Join(nginxDir, "lua", "security.lua")
	if _, err := os.Stat(luaPath); err == nil {
		result.Checks["security_lua_exists"] = true
		result.Details["security_lua_path"] = luaPath
	} else {
		result.Checks["security_lua_exists"] = false
		result.Issues = append(result.Issues, "security.lua does not exist at "+luaPath)
		result.Recommendations = append(result.Recommendations, "Enable realtime capture in Security settings to deploy security.lua")
	}

	// Check 2: nginx.conf exists and has Lua initialization
	nginxConfPath := filepath.Join(nginxDir, "nginx.conf")
	result.Details["nginx_conf_path"] = nginxConfPath
	if content, err := os.ReadFile(nginxConfPath); err == nil {
		result.Checks["nginx_conf_exists"] = true
		if strings.Contains(string(content), "init_by_lua_block") {
			result.Checks["nginx_conf_has_lua_init"] = true
		} else {
			result.Checks["nginx_conf_has_lua_init"] = false
			result.Issues = append(result.Issues, "nginx.conf does not have init_by_lua_block directive")
			result.Recommendations = append(result.Recommendations, "Enable realtime capture to generate Lua-enabled nginx.conf")
		}
	} else {
		result.Checks["nginx_conf_exists"] = false
		result.Checks["nginx_conf_has_lua_init"] = false
		result.Issues = append(result.Issues, "nginx.conf does not exist at "+nginxConfPath)
		result.Recommendations = append(result.Recommendations, "Enable realtime capture in Security settings")
	}

	// Check 3: blocked_ips.conf exists
	blockedIPsPath := filepath.Join(nginxDir, "conf.d", "blocked_ips.conf")
	if _, err := os.Stat(blockedIPsPath); err == nil {
		result.Checks["blocked_ips_conf_exists"] = true
	} else {
		result.Checks["blocked_ips_conf_exists"] = false
		result.Issues = append(result.Issues, "blocked_ips.conf does not exist")
	}

	// Check 4: rate_limits.conf exists
	rateLimitsPath := filepath.Join(nginxDir, "conf.d", "rate_limits.conf")
	if _, err := os.Stat(rateLimitsPath); err == nil {
		result.Checks["rate_limits_conf_exists"] = true
	} else {
		result.Checks["rate_limits_conf_exists"] = false
		result.Issues = append(result.Issues, "rate_limits.conf does not exist")
	}

	// Check 5: Nginx container is running and using correct config
	if m.config.Nginx.ContainerName != "" {
		result.Checks["nginx_container_running"] = m.isNginxContainerRunning()
		if !result.Checks["nginx_container_running"] {
			result.Issues = append(result.Issues, "Nginx container '"+m.config.Nginx.ContainerName+"' is not running")
		} else {
			// Check if nginx can load Lua module
			luaLoaded, luaErr := m.checkNginxLuaModule()
			result.Checks["nginx_lua_module_loaded"] = luaLoaded
			if !luaLoaded {
				if luaErr != "" {
					result.Issues = append(result.Issues, "Nginx Lua module check failed: "+luaErr)
				} else {
					result.Issues = append(result.Issues, "Nginx container does not have Lua module loaded")
				}
				result.Recommendations = append(result.Recommendations, "Ensure nginx container mounts "+nginxConfPath+" to /usr/local/openresty/nginx/conf/nginx.conf")
			}

			// Check if nginx.conf is mounted correctly
			configMounted, mountPath := m.checkNginxConfigMounted(nginxConfPath)
			result.Checks["nginx_conf_mounted"] = configMounted
			result.Details["nginx_mounted_config"] = mountPath
			if !configMounted {
				result.Issues = append(result.Issues, "Nginx container is not using the agent-generated nginx.conf")
				result.Recommendations = append(result.Recommendations,
					"Add volume mount to nginx docker-compose: "+nginxConfPath+":/usr/local/openresty/nginx/conf/nginx.conf:ro")
			}

			// Check if extra_hosts is configured (for Linux)
			hasExtraHosts := m.checkNginxExtraHosts()
			result.Checks["nginx_extra_hosts_configured"] = hasExtraHosts
			if !hasExtraHosts {
				result.Issues = append(result.Issues, "Nginx container may not be able to reach host.docker.internal")
				result.Recommendations = append(result.Recommendations,
					"Add extra_hosts to nginx docker-compose: - \"host.docker.internal:host-gateway\"")
			}
		}
	} else {
		result.Checks["nginx_container_running"] = false
		result.Issues = append(result.Issues, "Nginx container name not configured")
	}

	// Check 6: Vhosts have log_by_lua_block directive
	vhostsWithHook, vhostsWithoutHook := m.checkVhostsSecurityHook()
	result.Details["vhosts_with_security_hook"] = vhostsWithHook
	result.Details["vhosts_without_security_hook"] = vhostsWithoutHook
	if len(vhostsWithoutHook) > 0 {
		result.Checks["vhosts_have_security_hook"] = false
		result.Issues = append(result.Issues,
			fmt.Sprintf("%d vhost(s) missing log_by_lua_block: %v", len(vhostsWithoutHook), vhostsWithoutHook))
		result.Recommendations = append(result.Recommendations,
			"Add log_by_lua_block { security.capture_event() } to vhost server blocks, or use the regenerate vhosts API")
	} else if len(vhostsWithHook) > 0 {
		result.Checks["vhosts_have_security_hook"] = true
	} else {
		result.Checks["vhosts_have_security_hook"] = false
		result.Issues = append(result.Issues, "No vhost configurations found")
	}

	// Check 7: Lua directory is mounted in nginx container
	if m.config.Nginx.ContainerName != "" && result.Checks["nginx_container_running"] {
		luaMounted := m.checkNginxLuaDirectoryMounted()
		result.Checks["lua_directory_mounted"] = luaMounted
		if !luaMounted {
			result.Issues = append(result.Issues, "Lua directory not mounted in nginx container")
			result.Recommendations = append(result.Recommendations,
				"Add volume mount to nginx docker-compose: ./lua:/etc/nginx/lua:ro")
		}
	}

	// Determine overall status
	criticalChecks := []string{
		"security_lua_exists",
		"nginx_conf_has_lua_init",
		"nginx_container_running",
		"nginx_lua_module_loaded",
		"nginx_conf_mounted",
		"vhosts_have_security_hook",
		"lua_directory_mounted",
	}

	failedCritical := 0
	for _, check := range criticalChecks {
		if !result.Checks[check] {
			failedCritical++
		}
	}

	if failedCritical == 0 {
		result.Status = "healthy"
	} else if failedCritical <= 2 {
		result.Status = "degraded"
	} else {
		result.Status = "broken"
	}

	return result
}

func (m *Manager) isNginxContainerRunning() bool {
	cmd := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", m.config.Nginx.ContainerName)
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "true"
}

func (m *Manager) checkNginxLuaModule() (bool, string) {
	cmd := exec.Command("docker", "exec", m.config.Nginx.ContainerName, "sh", "-c",
		"cat /usr/local/openresty/nginx/conf/nginx.conf 2>/dev/null | grep -q init_by_lua_block")
	err := cmd.Run()
	if err != nil {
		// Try alternative path
		cmd2 := exec.Command("docker", "exec", m.config.Nginx.ContainerName, "sh", "-c",
			"cat /etc/nginx/nginx.conf 2>/dev/null | grep -q init_by_lua_block")
		err2 := cmd2.Run()
		if err2 != nil {
			return false, "init_by_lua_block not found in nginx config inside container"
		}
	}
	return true, ""
}

func (m *Manager) checkNginxConfigMounted(expectedPath string) (bool, string) {
	cmd := exec.Command("docker", "inspect", "-f",
		"{{range .Mounts}}{{if eq .Destination \"/usr/local/openresty/nginx/conf/nginx.conf\"}}{{.Source}}{{end}}{{end}}",
		m.config.Nginx.ContainerName)
	output, err := cmd.Output()
	if err != nil {
		return false, ""
	}

	mountedPath := strings.TrimSpace(string(output))
	if mountedPath == "" {
		return false, "not mounted"
	}

	// Check if the mounted path matches or contains the expected nginx.conf
	return strings.Contains(mountedPath, "nginx.conf") || mountedPath == expectedPath, mountedPath
}

func (m *Manager) checkNginxExtraHosts() bool {
	cmd := exec.Command("docker", "exec", m.config.Nginx.ContainerName, "sh", "-c",
		"getent hosts host.docker.internal 2>/dev/null || cat /etc/hosts | grep -q host.docker.internal")
	err := cmd.Run()
	return err == nil
}

func (m *Manager) checkVhostsSecurityHook() (withHook []string, withoutHook []string) {
	nginxDir := m.getNginxDir()
	confDir := filepath.Join(nginxDir, "conf.d")

	entries, err := os.ReadDir(confDir)
	if err != nil {
		return nil, nil
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".conf") {
			continue
		}
		// Skip security config files
		if entry.Name() == "blocked_ips.conf" || entry.Name() == "rate_limits.conf" {
			continue
		}

		confPath := filepath.Join(confDir, entry.Name())
		content, err := os.ReadFile(confPath)
		if err != nil {
			continue
		}

		vhostName := strings.TrimSuffix(entry.Name(), ".conf")
		if strings.Contains(string(content), "log_by_lua_block") {
			withHook = append(withHook, vhostName)
		} else {
			withoutHook = append(withoutHook, vhostName)
		}
	}

	return withHook, withoutHook
}

func (m *Manager) checkNginxLuaDirectoryMounted() bool {
	cmd := exec.Command("docker", "exec", m.config.Nginx.ContainerName, "sh", "-c",
		"test -f /etc/nginx/lua/security.lua && echo yes")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "yes"
}

// securityVolumeMounts are added when security is enabled and removed when disabled
var securityVolumeMounts = []string{
	"./nginx.conf:/usr/local/openresty/nginx/conf/nginx.conf:ro",
	"./lua:/etc/nginx/lua:ro",
}

func (m *Manager) getNginxComposePath() string {
	nginxDir := m.getNginxDir()
	composePath := filepath.Join(nginxDir, "docker-compose.yml")
	if _, err := os.Stat(composePath); err == nil {
		return composePath
	}
	composePath = filepath.Join(nginxDir, "docker-compose.yaml")
	if _, err := os.Stat(composePath); err == nil {
		return composePath
	}
	return ""
}

func (m *Manager) recreateNginxContainer() error {
	composePath := m.getNginxComposePath()
	if composePath == "" {
		return fmt.Errorf("nginx compose file not found")
	}

	nginxDir := m.getNginxDir()

	// Use docker compose to recreate the container
	cmd := exec.Command("docker", "compose", "-f", composePath, "up", "-d", "--force-recreate")
	cmd.Dir = nginxDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", string(output), err)
	}
	return nil
}

// addSecurityVolumeMountsInternal adds security volume mounts (caller must hold lock)
func (m *Manager) addSecurityVolumeMountsInternal() (bool, error) {
	composePath := m.getNginxComposePath()
	if composePath == "" {
		return false, fmt.Errorf("nginx compose file not found in %s", m.getNginxDir())
	}

	content, err := os.ReadFile(composePath)
	if err != nil {
		return false, fmt.Errorf("failed to read nginx compose file: %w", err)
	}

	// Service name in compose is always "nginx" per template
	serviceName := "nginx"

	updated := string(content)
	modified := false

	for _, mount := range securityVolumeMounts {
		if !docker.HasVolumeMount(updated, serviceName, mount) {
			newContent, err := docker.AddVolumeToService(updated, serviceName, mount)
			if err != nil {
				return false, fmt.Errorf("failed to add volume mount %s: %w", mount, err)
			}
			if newContent != updated {
				updated = newContent
				modified = true
			}
		}
	}

	if modified {
		if err := os.WriteFile(composePath, []byte(updated), 0644); err != nil {
			return false, fmt.Errorf("failed to write nginx compose file: %w", err)
		}
	}

	return modified, nil
}

// removeSecurityVolumeMountsInternal removes security volume mounts (caller must hold lock)
func (m *Manager) removeSecurityVolumeMountsInternal() (bool, error) {
	composePath := m.getNginxComposePath()
	if composePath == "" {
		return false, fmt.Errorf("nginx compose file not found in %s", m.getNginxDir())
	}

	content, err := os.ReadFile(composePath)
	if err != nil {
		return false, fmt.Errorf("failed to read nginx compose file: %w", err)
	}

	// Service name in compose is always "nginx" per template
	serviceName := "nginx"

	updated := string(content)
	modified := false

	for _, mount := range securityVolumeMounts {
		if docker.HasVolumeMount(updated, serviceName, mount) {
			newContent, err := docker.RemoveVolumeFromService(updated, serviceName, mount)
			if err != nil {
				return false, fmt.Errorf("failed to remove volume mount %s: %w", mount, err)
			}
			if newContent != updated {
				updated = newContent
				modified = true
			}
		}
	}

	if modified {
		if err := os.WriteFile(composePath, []byte(updated), 0644); err != nil {
			return false, fmt.Errorf("failed to write nginx compose file: %w", err)
		}
	}

	return modified, nil
}
