package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flatrun/agent/pkg/models"
)

type composeContainer struct {
	ID      string `json:"ID"`
	Name    string `json:"Name"`
	Service string `json:"Service"`
	State   string `json:"State"`
	Health  string `json:"Health"`
}

type Manager struct {
	discovery      *Discovery
	executor       *ComposeExecutor
	apiClient      *APIClient
	basePath       string
	cleanupTimeout time.Duration
	mu             sync.RWMutex
}

func (m *Manager) SetCleanupTimeout(d time.Duration) {
	if d > 0 {
		m.cleanupTimeout = d
	}
}

func (m *Manager) CleanupTimeout() time.Duration {
	if m.cleanupTimeout > 0 {
		return m.cleanupTimeout
	}
	return 2 * time.Minute
}

func NewManager(deploymentsPath string) *Manager {
	m := &Manager{
		discovery: NewDiscovery(deploymentsPath),
		executor:  NewComposeExecutor(deploymentsPath),
		basePath:  deploymentsPath,
	}
	if api, err := NewAPIClient(); err == nil {
		m.apiClient = api
	}
	return m
}

func (m *Manager) BasePath() string {
	return m.basePath
}

func (m *Manager) ListDeployments() ([]models.Deployment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	deployments, err := m.discovery.FindDeployments()
	if err != nil {
		return nil, err
	}

	for i := range deployments {
		status, _ := m.executor.GetStatus(deployments[i].Path)
		deployments[i].Status = status
	}

	return deployments, nil
}

func (m *Manager) GetDeployment(name string) (*models.Deployment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	deployment, err := m.discovery.GetDeployment(name)
	if err != nil {
		return nil, err
	}

	status, _ := m.executor.GetStatus(deployment.Path)
	deployment.Status = status

	m.populateContainerInfo(deployment)

	return deployment, nil
}

func (m *Manager) populateContainerInfo(deployment *models.Deployment) {
	output, err := m.executor.PS(deployment.Path)
	if err != nil {
		return
	}

	var containers []composeContainer
	trimmed := strings.TrimSpace(output)

	// Try parsing as JSON array first (newer docker compose versions)
	if strings.HasPrefix(trimmed, "[") {
		_ = json.Unmarshal([]byte(trimmed), &containers)
	}

	// Fallback: try newline-separated JSON objects (older versions)
	if len(containers) == 0 {
		for _, line := range strings.Split(output, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || line == "[]" {
				continue
			}
			var container composeContainer
			if err := json.Unmarshal([]byte(line), &container); err != nil {
				continue
			}
			containers = append(containers, container)
		}
	}

	for i := range deployment.Services {
		svc := &deployment.Services[i]
		for _, container := range containers {
			if container.Service == svc.Name {
				svc.ContainerID = container.ID
				svc.Status = container.State
				if container.Health != "" {
					svc.Health = container.Health
				}
				break
			}
		}
	}
}

func (m *Manager) CreateDeployment(name string, composeContent string, fileMounts []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.discovery.CreateDeployment(name, composeContent, fileMounts)
}

func (m *Manager) ApplyMountOwnership(name string, mounts []MountOwnership) error {
	deploymentPath := filepath.Join(m.basePath, name)
	return m.discovery.ApplyMountOwnership(deploymentPath, mounts)
}

func (m *Manager) DeleteDeployment(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	deployment, err := m.discovery.GetDeployment(name)
	if err != nil {
		return err
	}

	_, _ = m.executor.Down(deployment.Path)

	return m.discovery.DeleteDeployment(name)
}

// ensureContainerNames patches the compose file to set explicit container_name on all services.
func (m *Manager) ensureContainerNames(name string) {
	content, filename, err := m.discovery.GetComposeFile(name)
	if err != nil || content == "" {
		return
	}

	updated, err := EnsureContainerNames(content, name)
	if err != nil || updated == content {
		return
	}

	composePath := filepath.Join(m.basePath, name, filename)
	_ = os.WriteFile(composePath, []byte(updated), 0644)
}

func (m *Manager) StartDeployment(name string, opts ...RunOption) (string, error) {
	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return "", err
	}

	m.ensureContainerNames(name)

	output, err := m.executor.Up(deployment.Path, opts...)
	if err != nil {
		return output, err
	}

	go m.applyMountOwnershipFromContainer(name, deployment.Path)

	return output, nil
}

func (m *Manager) applyMountOwnershipFromContainer(name, deploymentPath string) {
	composeContent, _, err := m.discovery.GetComposeFile(name)
	if err != nil {
		return
	}

	bindMounts := ExtractBindMounts(composeContent)
	if len(bindMounts) == 0 {
		return
	}

	containerName := m.getMainContainerName(deploymentPath)
	if containerName == "" {
		containerName = name
	}

	user, err := InspectContainerUser(containerName)
	if err != nil {
		return
	}

	if user == "0:0" {
		return
	}

	var mounts []MountOwnership
	for _, path := range bindMounts {
		mounts = append(mounts, MountOwnership{
			HostPath: path,
			User:     user,
		})
	}

	_ = m.discovery.ApplyMountOwnership(deploymentPath, mounts)
}

func (m *Manager) getMainContainerName(deploymentPath string) string {
	output, err := m.executor.PS(deploymentPath)
	if err != nil {
		return ""
	}

	var containers []composeContainer
	trimmed := strings.TrimSpace(output)

	if strings.HasPrefix(trimmed, "[") {
		_ = json.Unmarshal([]byte(trimmed), &containers)
	}

	if len(containers) == 0 {
		for _, line := range strings.Split(output, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || line == "[]" {
				continue
			}
			var container composeContainer
			if err := json.Unmarshal([]byte(line), &container); err != nil {
				continue
			}
			containers = append(containers, container)
		}
	}

	for _, c := range containers {
		if c.Service == "app" || c.Service == "web" {
			return c.Name
		}
	}

	if len(containers) > 0 {
		return containers[0].Name
	}

	return ""
}

func (m *Manager) snapshotBindMounts(name, deploymentPath string) string {
	composeContent, _, err := m.discovery.GetComposeFile(name)
	if err != nil {
		return ""
	}

	bindMounts := ExtractBindMounts(composeContent)
	if len(bindMounts) == 0 {
		return ""
	}

	snapshotDir, err := os.MkdirTemp("", "flatrun-snapshot-*")
	if err != nil {
		return ""
	}

	hasData := false
	for _, mount := range bindMounts {
		srcPath := filepath.Join(deploymentPath, mount)
		if info, err := os.Stat(srcPath); err != nil || !info.IsDir() {
			continue
		}
		destPath := filepath.Join(snapshotDir, mount)
		if err := copyDir(srcPath, destPath); err == nil {
			hasData = true
		}
	}

	if !hasData {
		os.RemoveAll(snapshotDir)
		return ""
	}
	return snapshotDir
}

func (m *Manager) restoreBindMounts(deploymentPath, snapshotDir string) {
	if snapshotDir == "" {
		return
	}
	defer os.RemoveAll(snapshotDir)

	_ = filepath.Walk(snapshotDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || path == snapshotDir {
			return nil
		}
		relPath, err := filepath.Rel(snapshotDir, path)
		if err != nil {
			log.Printf("Restore: failed to compute relative path for %s: %v", path, err)
			return nil
		}
		destPath := filepath.Join(deploymentPath, relPath)

		if info.IsDir() {
			if err := os.MkdirAll(destPath, info.Mode()); err != nil {
				log.Printf("Restore: failed to create directory %s: %v", relPath, err)
			}
			return nil
		}

		if _, err := os.Stat(destPath); err == nil {
			return nil
		}
		if err := copyFile(path, destPath); err != nil {
			log.Printf("Restore: failed to restore file %s: %v", relPath, err)
		}
		return nil
	})
}

func (m *Manager) StopDeployment(name string) (string, error) {
	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return "", err
	}

	return m.executor.Stop(deployment.Path)
}

func (m *Manager) RestartDeployment(name string, opts ...RunOption) (string, error) {
	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return "", err
	}

	m.ensureContainerNames(name)

	snapshotDir := m.snapshotBindMounts(name, deployment.Path)

	output, err := m.executor.Restart(deployment.Path, opts...)
	if err != nil {
		m.restoreBindMounts(deployment.Path, snapshotDir)
		return output, err
	}

	go func() {
		m.applyMountOwnershipFromContainer(name, deployment.Path)
		m.restoreBindMounts(deployment.Path, snapshotDir)
	}()

	return output, nil
}

func (m *Manager) RebuildDeployment(name string, opts ...RunOption) (string, error) {
	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return "", err
	}

	m.ensureContainerNames(name)

	snapshotDir := m.snapshotBindMounts(name, deployment.Path)

	output, err := m.executor.Rebuild(deployment.Path, opts...)
	if err != nil {
		m.restoreBindMounts(deployment.Path, snapshotDir)
		return output, err
	}

	go func() {
		m.applyMountOwnershipFromContainer(name, deployment.Path)
		m.restoreBindMounts(deployment.Path, snapshotDir)
	}()

	return output, nil
}

func (m *Manager) StartService(name, service string, opts ...RunOption) (string, error) {
	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return "", err
	}

	m.ensureContainerNames(name)
	return m.executor.StartService(deployment.Path, service, opts...)
}

func (m *Manager) StopService(name, service string, opts ...RunOption) (string, error) {
	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return "", err
	}

	return m.executor.StopService(deployment.Path, service, opts...)
}

func (m *Manager) RestartService(name, service string, opts ...RunOption) (string, error) {
	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return "", err
	}

	return m.executor.RestartService(deployment.Path, service, opts...)
}

func (m *Manager) RebuildService(name, service string, opts ...RunOption) (string, error) {
	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return "", err
	}

	m.ensureContainerNames(name)
	return m.executor.RebuildService(deployment.Path, service, opts...)
}

func (m *Manager) PullService(name, service string, opts ...RunOption) (string, error) {
	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return "", err
	}

	return m.executor.PullService(deployment.Path, service, opts...)
}

func (m *Manager) PullDeployment(name string, onlyLatest bool, opts ...RunOption) (string, error) {
	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return "", err
	}

	return m.executor.Pull(deployment.Path, onlyLatest, opts...)
}

func (m *Manager) GetDeploymentImages(name string) ([]ImageInfo, error) {
	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return nil, err
	}

	return m.executor.GetImageInfo(deployment.Path)
}

func (m *Manager) ExecuteQuickAction(name string, actionID string) (string, error) {
	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return "", err
	}

	if deployment.Metadata == nil || len(deployment.Metadata.QuickActions) == 0 {
		return "", fmt.Errorf("no quick actions defined for deployment")
	}

	var action *models.QuickAction
	for _, a := range deployment.Metadata.QuickActions {
		if a.ID == actionID {
			actionCopy := a
			action = &actionCopy
			break
		}
	}

	if action == nil {
		return "", fmt.Errorf("quick action not found: %s", actionID)
	}

	m.populateContainerInfo(deployment)

	var containerID string
	serviceName := action.Service

	if serviceName != "" {
		for _, svc := range deployment.Services {
			if svc.Name == serviceName && svc.ContainerID != "" {
				containerID = svc.ContainerID
				break
			}
		}
	}

	if containerID == "" {
		for _, svc := range deployment.Services {
			if svc.ContainerID != "" {
				containerID = svc.ContainerID
				break
			}
		}
	}

	if containerID == "" {
		if serviceName != "" {
			return "", fmt.Errorf("no running container found for service: %s", serviceName)
		}
		return "", fmt.Errorf("no running containers found in deployment")
	}

	return m.executor.ExecCommand(containerID, action.Command)
}

func (m *Manager) GetComposeServices(name string) ([]models.Service, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	deployment, err := m.discovery.GetDeployment(name)
	if err != nil {
		return nil, err
	}

	return deployment.Services, nil
}

func (m *Manager) GetComposeServiceNames(name string) ([]string, error) {
	services, err := m.GetComposeServices(name)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(services))
	for i, s := range services {
		names[i] = s.Name
	}
	return names, nil
}

func (m *Manager) ResolveService(name string, serviceName string) (string, error) {
	serviceNames, err := m.GetComposeServiceNames(name)
	if err != nil || len(serviceNames) == 0 {
		return "", fmt.Errorf("no services found in compose file")
	}

	if serviceName != "" {
		for _, sn := range serviceNames {
			if sn == serviceName {
				return serviceName, nil
			}
		}
		return "", fmt.Errorf("service '%s' not found in compose file, available: %s", serviceName, strings.Join(serviceNames, ", "))
	}

	if len(serviceNames) == 1 {
		return serviceNames[0], nil
	}

	for _, sn := range serviceNames {
		if sn == "app" {
			return "app", nil
		}
	}

	return "", fmt.Errorf("multiple services found (%s), please specify which service to use", strings.Join(serviceNames, ", "))
}

func (m *Manager) ComposeExec(ctx context.Context, name string, service string, command string) (string, error) {
	if m.apiClient == nil {
		return "", fmt.Errorf("docker API client not available")
	}

	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return "", err
	}

	project := m.executor.getProjectName(deployment.Path)
	return m.apiClient.ExecInService(ctx, project, service, command)
}

func (m *Manager) GetDeploymentLogs(name string, tail int) (string, error) {
	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return "", err
	}

	return m.executor.Logs(deployment.Path, tail)
}

func (m *Manager) UpdateDeployment(name string, composeContent string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.discovery.UpdateComposeFile(name, composeContent)
}

func (m *Manager) GetComposeFile(name string) (string, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.discovery.GetComposeFile(name)
}

func (m *Manager) SaveMetadata(name string, metadata *models.ServiceMetadata) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.discovery.SaveMetadata(name, metadata)
}

type DeploymentStats struct {
	TotalDeployments int       `json:"total_deployments"`
	Running          int       `json:"running"`
	Stopped          int       `json:"stopped"`
	Error            int       `json:"error"`
	Unknown          int       `json:"unknown"`
	LastUpdated      time.Time `json:"last_updated"`
}

func (m *Manager) GetStats() (*DeploymentStats, error) {
	deployments, err := m.ListDeployments()
	if err != nil {
		return nil, err
	}

	stats := &DeploymentStats{
		TotalDeployments: len(deployments),
		LastUpdated:      time.Now(),
	}

	for _, d := range deployments {
		switch d.Status {
		case "running":
			stats.Running++
		case "stopped":
			stats.Stopped++
		case "error":
			stats.Error++
		default:
			stats.Unknown++
		}
	}

	return stats, nil
}

func (m *Manager) ListInfrastructure() ([]models.Deployment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	deployments, err := m.discovery.FindInfrastructure()
	if err != nil {
		return nil, err
	}

	for i := range deployments {
		status, _ := m.executor.GetStatus(deployments[i].Path)
		deployments[i].Status = status
	}

	return deployments, nil
}
