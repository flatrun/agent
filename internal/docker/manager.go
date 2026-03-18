package docker

import (
	"encoding/json"
	"fmt"
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
	discovery *Discovery
	executor  *ComposeExecutor
	basePath  string
	mu        sync.RWMutex
}

func NewManager(deploymentsPath string) *Manager {
	return &Manager{
		discovery: NewDiscovery(deploymentsPath),
		executor:  NewComposeExecutor(deploymentsPath),
		basePath:  deploymentsPath,
	}
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

func (m *Manager) CreateDeployment(name string, composeContent string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.discovery.CreateDeployment(name, composeContent)
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

func (m *Manager) StartDeployment(name string) (string, error) {
	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return "", err
	}

	m.ensureContainerNames(name)

	output, err := m.executor.Up(deployment.Path)
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

func (m *Manager) StopDeployment(name string) (string, error) {
	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return "", err
	}

	return m.executor.Stop(deployment.Path)
}

func (m *Manager) RestartDeployment(name string) (string, error) {
	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return "", err
	}

	m.ensureContainerNames(name)

	return m.executor.Restart(deployment.Path)
}

func (m *Manager) RebuildDeployment(name string) (string, error) {
	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return "", err
	}

	m.ensureContainerNames(name)

	output, err := m.executor.Rebuild(deployment.Path)
	if err != nil {
		return output, err
	}

	go m.applyMountOwnershipFromContainer(name, deployment.Path)

	return output, nil
}

func (m *Manager) PullDeployment(name string, onlyLatest bool) (string, error) {
	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return "", err
	}

	return m.executor.Pull(deployment.Path, onlyLatest)
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
