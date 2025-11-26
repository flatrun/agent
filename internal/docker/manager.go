package docker

import (
	"sync"
	"time"

	"github.com/flatrun/agent/pkg/models"
)

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

	return deployment, nil
}

func (m *Manager) CreateDeployment(name string, composeContent string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.discovery.CreateDeployment(name, composeContent)
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

func (m *Manager) StartDeployment(name string) (string, error) {
	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return "", err
	}

	return m.executor.Up(deployment.Path)
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

	return m.executor.Restart(deployment.Path)
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

func (m *Manager) GetComposeFile(name string) (string, error) {
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
