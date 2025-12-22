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

		// Create lua directory and write security.lua with injected agent IP
		if err := os.MkdirAll(luaDir, 0755); err != nil {
			errors = append(errors, fmt.Sprintf("failed to create lua directory: %v", err))
		} else {
			agentIP := m.GetDockerHostIP()
			agentPort := m.GetAgentPort()
			result["agent_ip"] = agentIP
			result["agent_port"] = agentPort

			securityLua, err := templates.GetNginxSecurityLuaWithConfig(agentIP, agentPort)
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

			trafficLua, err := templates.GetNginxTrafficLuaWithConfig(agentIP, agentPort)
			if err != nil {
				errors = append(errors, fmt.Sprintf("failed to get traffic.lua template: %v", err))
			} else {
				luaPath := filepath.Join(luaDir, "traffic.lua")
				if err := os.WriteFile(luaPath, trafficLua, 0644); err != nil {
					errors = append(errors, fmt.Sprintf("failed to write traffic.lua: %v", err))
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

// GetDockerHostIP returns the IP address that containers can use to reach the host.
// It tries multiple methods and falls back to the default Docker bridge gateway.
func (m *Manager) GetDockerHostIP() string {
	// Method 1: Try to get host.docker.internal from nginx container's /etc/hosts
	if m.config.Nginx.ContainerName != "" && m.IsNginxRunning() {
		cmd := exec.Command("docker", "exec", m.config.Nginx.ContainerName, "sh", "-c",
			"getent hosts host.docker.internal 2>/dev/null | awk '{print $1}'")
		if output, err := cmd.Output(); err == nil {
			ip := strings.TrimSpace(string(output))
			if ip != "" && ip != "host.docker.internal" {
				return ip
			}
		}

		// Also try grepping /etc/hosts
		cmd = exec.Command("docker", "exec", m.config.Nginx.ContainerName, "sh", "-c",
			"grep host.docker.internal /etc/hosts 2>/dev/null | awk '{print $1}'")
		if output, err := cmd.Output(); err == nil {
			ip := strings.TrimSpace(string(output))
			if ip != "" {
				return ip
			}
		}
	}

	// Method 2: Try to get the Docker bridge gateway IP
	cmd := exec.Command("docker", "network", "inspect", "bridge", "-f",
		"{{range .IPAM.Config}}{{.Gateway}}{{end}}")
	if output, err := cmd.Output(); err == nil {
		ip := strings.TrimSpace(string(output))
		if ip != "" {
			return ip
		}
	}

	// Fallback: Default Docker bridge gateway
	return "172.17.0.1"
}

// GetAgentPort returns the port the agent API is listening on
func (m *Manager) GetAgentPort() int {
	if m.config.API.Port > 0 {
		return m.config.API.Port
	}
	return 8090
}

func (m *Manager) reloadNginx() error {
	if err := m.waitForContainerReady(5); err != nil {
		return fmt.Errorf("container not ready: %w", err)
	}

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

func (m *Manager) waitForContainerReady(maxRetries int) error {
	containerName := m.config.Nginx.ContainerName
	for i := 0; i < maxRetries; i++ {
		cmd := exec.Command("docker", "inspect", "-f", "{{.State.Status}}", containerName)
		output, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("failed to get container status: %w", err)
		}

		status := strings.TrimSpace(string(output))
		if status == "running" {
			cmd = exec.Command("docker", "inspect", "-f", "{{.State.Restarting}}", containerName)
			output, err = cmd.Output()
			if err == nil && strings.TrimSpace(string(output)) == "false" {
				return nil
			}
		}

		if i < maxRetries-1 {
			time.Sleep(time.Second)
		}
	}
	return fmt.Errorf("container %s not ready after %d attempts", containerName, maxRetries)
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

	// Check 1: security.lua exists and has correct agent IP
	securityLuaPath := filepath.Join(nginxDir, "lua", "security.lua")
	if content, err := os.ReadFile(securityLuaPath); err == nil {
		result.Checks["security_lua_exists"] = true
		result.Details["security_lua_path"] = securityLuaPath

		// Check if agent IP is properly configured
		if strings.Contains(string(content), "host.docker.internal") {
			result.Checks["security_lua_ip_injected"] = false
			result.Issues = append(result.Issues, "Agent connection not configured in security module")
			result.Recommendations = append(result.Recommendations, "Click 'Regenerate Scripts' in Security settings to configure agent connection")
		} else {
			result.Checks["security_lua_ip_injected"] = true
		}
	} else {
		result.Checks["security_lua_exists"] = false
		result.Checks["security_lua_ip_injected"] = false
		result.Issues = append(result.Issues, "security.lua does not exist at "+securityLuaPath)
		result.Recommendations = append(result.Recommendations, "Enable realtime capture in Security settings to deploy security.lua")
	}

	// Check 1b: traffic.lua exists and has correct agent IP
	trafficLuaPath := filepath.Join(nginxDir, "lua", "traffic.lua")
	if content, err := os.ReadFile(trafficLuaPath); err == nil {
		result.Checks["traffic_lua_exists"] = true
		result.Details["traffic_lua_path"] = trafficLuaPath

		if strings.Contains(string(content), "host.docker.internal") {
			result.Checks["traffic_lua_ip_injected"] = false
			result.Issues = append(result.Issues, "Agent connection not configured in traffic module")
			result.Recommendations = append(result.Recommendations, "Click 'Regenerate Scripts' in Security settings to configure agent connection")
		} else {
			result.Checks["traffic_lua_ip_injected"] = true
		}
	} else {
		result.Checks["traffic_lua_exists"] = false
		result.Checks["traffic_lua_ip_injected"] = false
		result.Issues = append(result.Issues, "traffic.lua does not exist at "+trafficLuaPath)
		result.Recommendations = append(result.Recommendations, "Enable realtime capture to deploy traffic.lua for request logging")
	}

	// Check 1c: Agent IP detection works
	agentIP := m.GetDockerHostIP()
	result.Details["detected_agent_ip"] = agentIP
	result.Details["agent_port"] = m.GetAgentPort()
	if agentIP != "" {
		result.Checks["agent_ip_detected"] = true
	} else {
		result.Checks["agent_ip_detected"] = false
		result.Issues = append(result.Issues, "Unable to detect agent network address")
	}

	// Check 2: nginx.conf exists and has Lua initialization
	nginxConfPath := filepath.Join(nginxDir, "nginx.conf")
	result.Details["nginx_conf_path"] = nginxConfPath
	if content, err := os.ReadFile(nginxConfPath); err == nil {
		result.Checks["nginx_conf_exists"] = true
		contentStr := string(content)

		if strings.Contains(contentStr, "init_by_lua_block") {
			result.Checks["nginx_conf_has_lua_init"] = true
		} else {
			result.Checks["nginx_conf_has_lua_init"] = false
			result.Issues = append(result.Issues, "nginx.conf does not have init_by_lua_block directive")
			result.Recommendations = append(result.Recommendations, "Enable realtime capture to generate Lua-enabled nginx.conf")
		}

		// Check for traffic module loading
		if strings.Contains(contentStr, "traffic = require") || strings.Contains(contentStr, "traffic.log_request") {
			result.Checks["nginx_conf_has_traffic_module"] = true
		} else {
			result.Checks["nginx_conf_has_traffic_module"] = false
			result.Issues = append(result.Issues, "nginx.conf does not load traffic module for request logging")
			result.Recommendations = append(result.Recommendations, "Use POST /api/security/refresh to regenerate nginx.conf with traffic logging")
		}

		// Check for global traffic logging
		if strings.Contains(contentStr, "log_by_lua_block") && strings.Contains(contentStr, "traffic.log_request") {
			result.Checks["nginx_conf_has_global_traffic_logging"] = true
		} else {
			result.Checks["nginx_conf_has_global_traffic_logging"] = false
			result.Issues = append(result.Issues, "nginx.conf does not have global traffic logging enabled")
		}
	} else {
		result.Checks["nginx_conf_exists"] = false
		result.Checks["nginx_conf_has_lua_init"] = false
		result.Checks["nginx_conf_has_traffic_module"] = false
		result.Checks["nginx_conf_has_global_traffic_logging"] = false
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

			// Check if nginx can reach the agent
			hasExtraHosts := m.checkNginxExtraHosts()
			result.Checks["nginx_extra_hosts_configured"] = hasExtraHosts
			if !hasExtraHosts {
				result.Issues = append(result.Issues, "Nginx container cannot reach the agent")
				result.Recommendations = append(result.Recommendations,
					"Configure network access in your nginx docker-compose file")
			}
		}
	} else {
		result.Checks["nginx_container_running"] = false
		result.Issues = append(result.Issues, "Nginx container name not configured")
	}

	// Check 6: Vhosts with security enabled have log_by_lua_block directive
	vhostsWithHook, vhostsWithoutHook := m.checkVhostsSecurityHook()
	deploymentsWithSecurityEnabled := m.getDeploymentsWithSecurityEnabled()

	result.Details["vhosts_with_security_hook"] = vhostsWithHook
	result.Details["vhosts_without_security_hook"] = vhostsWithoutHook
	result.Details["deployments_with_security_enabled"] = deploymentsWithSecurityEnabled

	// Find vhosts that SHOULD have hooks but don't
	var missingHooks []string
	for _, dep := range deploymentsWithSecurityEnabled {
		hasHook := false
		for _, v := range vhostsWithHook {
			if v == dep {
				hasHook = true
				break
			}
		}
		if !hasHook {
			missingHooks = append(missingHooks, dep)
		}
	}

	result.Details["vhosts_missing_required_hooks"] = missingHooks

	if len(missingHooks) > 0 {
		result.Checks["vhosts_have_security_hook"] = false
		result.Issues = append(result.Issues,
			fmt.Sprintf("%d deployment(s) have security enabled but vhost missing hooks: %v", len(missingHooks), missingHooks))
		result.Recommendations = append(result.Recommendations,
			"Use PUT /api/deployments/:name/security to regenerate vhost with security hooks")
	} else if len(deploymentsWithSecurityEnabled) > 0 {
		result.Checks["vhosts_have_security_hook"] = true
	} else {
		// No deployments have security enabled - that's fine, hooks not required
		result.Checks["vhosts_have_security_hook"] = true
		result.Details["note"] = "No deployments have per-deployment security enabled (traffic logging still works globally)"
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

	// Check 8: DNS/Connectivity - Can nginx reach the agent?
	if m.config.Nginx.ContainerName != "" && result.Checks["nginx_container_running"] {
		agentIP := m.GetDockerHostIP()
		agentPort := m.GetAgentPort()
		canReachAgent := m.checkNginxCanReachAgent(agentIP, agentPort)
		result.Checks["nginx_can_reach_agent"] = canReachAgent
		result.Details["connectivity_test_ip"] = agentIP
		result.Details["connectivity_test_port"] = agentPort

		if !canReachAgent {
			result.Issues = append(result.Issues,
				fmt.Sprintf("Nginx container cannot reach agent at %s:%d - Lua scripts will fail to send events", agentIP, agentPort))
			result.Recommendations = append(result.Recommendations,
				"1. Check if agent is running and listening on the correct port")
			result.Recommendations = append(result.Recommendations,
				"2. Ensure nginx container has network access to host (extra_hosts or host network mode)")
			result.Recommendations = append(result.Recommendations,
				"3. Use POST /api/security/refresh to regenerate scripts with correct IP")
		}
	}

	// Determine overall status
	criticalChecks := []string{
		"security_lua_exists",
		"security_lua_ip_injected",
		"traffic_lua_exists",
		"traffic_lua_ip_injected",
		"nginx_conf_has_lua_init",
		"nginx_container_running",
		"nginx_lua_module_loaded",
		"nginx_conf_mounted",
		"lua_directory_mounted",
		"nginx_can_reach_agent",
		"vhosts_have_security_hook",
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

// getDeploymentsWithSecurityEnabled reads deployment metadata to find which have security enabled
func (m *Manager) getDeploymentsWithSecurityEnabled() []string {
	var enabled []string

	entries, err := os.ReadDir(m.config.DeploymentsPath)
	if err != nil {
		return enabled
	}

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		metadataPath := filepath.Join(m.config.DeploymentsPath, entry.Name(), "service.yml")
		content, err := os.ReadFile(metadataPath)
		if err != nil {
			continue
		}

		contentStr := string(content)
		if strings.Contains(contentStr, "security:") &&
			(strings.Contains(contentStr, "enabled: true") || strings.Contains(contentStr, "enabled: \"true\"")) {
			enabled = append(enabled, entry.Name())
		}
	}

	return enabled
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
		"test -f /etc/nginx/lua/security.lua && test -f /etc/nginx/lua/traffic.lua && echo yes")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "yes"
}

// checkNginxCanReachAgent tests if nginx container can reach the agent API
func (m *Manager) checkNginxCanReachAgent(agentIP string, agentPort int) bool {
	// Try to connect to agent health endpoint from nginx container
	// Use wget or curl depending on what's available in the container
	testCmd := fmt.Sprintf(
		"wget -q -O /dev/null --timeout=2 http://%s:%d/api/health 2>/dev/null && echo yes || "+
			"curl -s --connect-timeout 2 http://%s:%d/api/health >/dev/null 2>&1 && echo yes || "+
			"echo no",
		agentIP, agentPort, agentIP, agentPort)

	cmd := exec.Command("docker", "exec", m.config.Nginx.ContainerName, "sh", "-c", testCmd)
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(output), "yes")
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

// RefreshSecurityScriptsResult contains the result of refreshing security scripts
type RefreshSecurityScriptsResult struct {
	Success            bool     `json:"success"`
	AgentIP            string   `json:"agent_ip"`
	AgentPort          int      `json:"agent_port"`
	NginxConfWritten   bool     `json:"nginx_conf_written"`
	LuaWritten         bool     `json:"lua_written"`
	VolumesModified    bool     `json:"volumes_modified"`
	ContainerRecreated bool     `json:"container_recreated"`
	NginxReloaded      bool     `json:"nginx_reloaded"`
	VhostsUpdated      []string `json:"vhosts_updated,omitempty"`
	Errors             []string `json:"errors,omitempty"`
}

// RefreshSecurityScripts regenerates all security configs: nginx.conf, Lua scripts, and vhosts
func (m *Manager) RefreshSecurityScripts() (*RefreshSecurityScriptsResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := &RefreshSecurityScriptsResult{
		Success:       true,
		Errors:        []string{},
		VhostsUpdated: []string{},
	}

	// Get agent IP and port
	agentIP := m.GetDockerHostIP()
	agentPort := m.GetAgentPort()
	result.AgentIP = agentIP
	result.AgentPort = agentPort

	nginxDir := m.getNginxDir()
	if nginxDir == "" {
		result.Errors = append(result.Errors, "nginx config path not configured")
		result.Success = false
		return result, fmt.Errorf("nginx config path not configured")
	}

	luaDir := filepath.Join(nginxDir, "lua")
	confPath := filepath.Join(nginxDir, "nginx.conf")

	// Create directories
	if err := os.MkdirAll(luaDir, 0755); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("failed to create lua directory: %v", err))
		result.Success = false
		return result, err
	}

	confDir := filepath.Join(nginxDir, "conf.d")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("failed to create conf.d directory: %v", err))
	}

	// Write nginx.conf with Lua support
	nginxConf, err := templates.GetNginxConfig(true)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("failed to get nginx lua config template: %v", err))
	} else {
		if err := os.WriteFile(confPath, nginxConf, 0644); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("failed to write nginx.conf: %v", err))
		} else {
			result.NginxConfWritten = true
		}
	}

	// Generate and write security.lua with injected IP
	securityLua, err := templates.GetNginxSecurityLuaWithConfig(agentIP, agentPort)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("failed to generate security.lua: %v", err))
		result.Success = false
		return result, err
	}

	securityLuaPath := filepath.Join(luaDir, "security.lua")
	if err := os.WriteFile(securityLuaPath, securityLua, 0644); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("failed to write security.lua: %v", err))
		result.Success = false
		return result, err
	}
	result.LuaWritten = true

	// Generate and write traffic.lua with injected IP
	trafficLua, err := templates.GetNginxTrafficLuaWithConfig(agentIP, agentPort)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("failed to generate traffic.lua: %v", err))
	} else {
		trafficLuaPath := filepath.Join(luaDir, "traffic.lua")
		if err := os.WriteFile(trafficLuaPath, trafficLua, 0644); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("failed to write traffic.lua: %v", err))
		}
	}

	// Ensure blocked_ips.conf exists
	blockedIPsPath := filepath.Join(confDir, "blocked_ips.conf")
	if _, err := os.Stat(blockedIPsPath); os.IsNotExist(err) {
		content := "# Auto-generated - No blocked IPs\n"
		if err := os.WriteFile(blockedIPsPath, []byte(content), 0644); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("failed to create blocked_ips.conf: %v", err))
		}
	}

	// Ensure rate_limits.conf exists
	rateLimitsPath := filepath.Join(confDir, "rate_limits.conf")
	if _, err := os.Stat(rateLimitsPath); os.IsNotExist(err) {
		content := "# Auto-generated - No rate limit zones\n"
		if err := os.WriteFile(rateLimitsPath, []byte(content), 0644); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("failed to create rate_limits.conf: %v", err))
		}
	}

	// Add volume mounts to docker-compose if needed
	volumesModified, volumeErr := m.addSecurityVolumeMountsInternal()
	if volumeErr != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("failed to modify volume mounts: %v", volumeErr))
	}
	result.VolumesModified = volumesModified

	// Recreate or reload nginx container
	if volumesModified {
		if err := m.recreateNginxContainer(); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("failed to recreate nginx container: %v", err))
		} else {
			result.ContainerRecreated = true
		}
	} else if m.IsNginxRunning() {
		if err := m.reloadNginx(); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("failed to reload nginx: %v", err))
		} else {
			result.NginxReloaded = true
		}
	}

	return result, nil
}
