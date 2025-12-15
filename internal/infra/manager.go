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
	m.mu.Lock()
	defer m.mu.Unlock()

	nginxDir := m.getNginxDir()
	if nginxDir == "" {
		return fmt.Errorf("nginx config path not configured")
	}

	if err := os.MkdirAll(nginxDir, 0755); err != nil {
		return fmt.Errorf("failed to create nginx directory: %w", err)
	}

	nginxConf, err := templates.GetNginxConfig(enabled)
	if err != nil {
		return fmt.Errorf("failed to get nginx config template: %w", err)
	}

	confPath := filepath.Join(nginxDir, "nginx.conf")
	if err := os.WriteFile(confPath, nginxConf, 0644); err != nil {
		return fmt.Errorf("failed to write nginx.conf: %w", err)
	}

	if enabled {
		luaDir := filepath.Join(nginxDir, "lua")
		if err := os.MkdirAll(luaDir, 0755); err != nil {
			return fmt.Errorf("failed to create lua directory: %w", err)
		}

		securityLua, err := templates.GetNginxSecurityLua()
		if err != nil {
			return fmt.Errorf("failed to get security.lua template: %w", err)
		}

		luaPath := filepath.Join(luaDir, "security.lua")
		if err := os.WriteFile(luaPath, securityLua, 0644); err != nil {
			return fmt.Errorf("failed to write security.lua: %w", err)
		}
	}

	confDir := filepath.Join(nginxDir, "conf.d")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		return fmt.Errorf("failed to create conf.d directory: %w", err)
	}

	blockedIPsPath := filepath.Join(confDir, "blocked_ips.conf")
	if _, err := os.Stat(blockedIPsPath); os.IsNotExist(err) {
		content := "# Auto-generated by FlatRun Security\n# No blocked IPs\n"
		if err := os.WriteFile(blockedIPsPath, []byte(content), 0644); err != nil {
			return fmt.Errorf("failed to create blocked_ips.conf: %w", err)
		}
	}

	rateLimitsPath := filepath.Join(confDir, "rate_limits.conf")
	if _, err := os.Stat(rateLimitsPath); os.IsNotExist(err) {
		content := "# Auto-generated by FlatRun Security\n# No rate limit zones defined\n"
		if err := os.WriteFile(rateLimitsPath, []byte(content), 0644); err != nil {
			return fmt.Errorf("failed to create rate_limits.conf: %w", err)
		}
	}

	if m.config.Nginx.ContainerName != "" {
		if err := m.reloadNginx(); err != nil {
			return fmt.Errorf("failed to reload nginx: %w", err)
		}
	}

	return nil
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
