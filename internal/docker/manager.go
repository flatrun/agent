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
	"sync/atomic"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/flatrun/agent/pkg/models"
)

type composeContainer struct {
	ID      string `json:"ID"`
	Name    string `json:"Name"`
	Service string `json:"Service"`
	State   string `json:"State"`
	Health  string `json:"Health"`
}

// statusReadTimeout bounds the Engine API call backing a status read, so a
// wedged daemon degrades a list request rather than hanging it.
const statusReadTimeout = 10 * time.Second

// containerIndex groups a host's live compose containers by project name.
type containerIndex map[string][]container.Summary

// indexContainersByProject reads every live compose container on the host in one
// Engine API call and groups it by project. Deriving status from this index
// keeps a list request at a single Docker round-trip regardless of how many
// deployments exist, where the compose CLI costs several processes each.
func (m *Manager) indexContainersByProject(ctx context.Context) (containerIndex, error) {
	if m.apiClient == nil {
		return nil, fmt.Errorf("docker api client unavailable")
	}

	summaries, err := m.apiClient.ListLiveComposeContainers(ctx)
	if err != nil {
		return nil, err
	}

	index := make(containerIndex)
	for _, summary := range summaries {
		if project := summary.Labels[composeProjectLabel]; project != "" {
			index[project] = append(index[project], summary)
		}
	}
	return index, nil
}

// ContainerPrimaryIP returns the first running container's address for a
// deployment on the given docker network. A flatrun deploy names its compose
// project after the deployment, so the project name is the deployment name.
func (m *Manager) ContainerPrimaryIP(project, network string) (string, error) {
	if m.apiClient == nil {
		return "", fmt.Errorf("docker api client unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), statusReadTimeout)
	defer cancel()
	return m.apiClient.ContainerPrimaryIP(ctx, project, network)
}

// projectFor resolves a deployment's compose project name without shelling out.
// It mirrors ComposeExecutor.getProjectName, except that the fallback probe for
// an existing project reads the already-fetched index instead of running
// `docker compose ps` once per candidate.
func (m *Manager) projectFor(deployment *models.Deployment, index containerIndex) string {
	if name := m.executor.readComposeProjectName(deployment.Path); name != "" {
		return name
	}

	dirName := filepath.Base(strings.TrimSuffix(deployment.Path, "/"))
	for _, candidate := range []string{dirName, "flatrun-" + dirName} {
		if len(index[candidate]) > 0 {
			return candidate
		}
	}
	return dirName
}

// statusFromContainers collapses a project's live containers into a deployment
// status. Stopped containers are absent from the index by construction, so a
// project with none is stopped. A paused container counts as running because it
// is still up, which is what the compose CLI path reported.
func statusFromContainers(containers []container.Summary) string {
	if len(containers) == 0 {
		return string(models.StatusStopped)
	}

	for _, c := range containers {
		if c.State == container.StateRunning || c.State == container.StatePaused {
			return string(models.StatusRunning)
		}
	}

	// Containers exist but none are up, e.g. restarting or being removed.
	return string(models.StatusUnknown)
}

// healthFromStatus extracts a service's health from the Engine API's human
// readable status ("Up 2 hours (healthy)"), which is where the daemon reports
// it; `compose ps --format json` exposes it as a discrete field instead. Only
// the daemon's three health renderings count: other parenthesised suffixes,
// such as "(Paused)", are not health.
func healthFromStatus(status string) string {
	switch {
	case strings.Contains(status, "(healthy)"):
		return "healthy"
	case strings.Contains(status, "(unhealthy)"):
		return "unhealthy"
	case strings.Contains(status, "(health: starting)"):
		return "starting"
	}
	return ""
}

// shortContainerID trims a container ID to the 12-character form Docker itself
// abbreviates to, which is what the compose CLI reports and therefore what
// clients already receive.
func shortContainerID(id string) string {
	const shortIDLength = 12
	if len(id) <= shortIDLength {
		return id
	}
	return id[:shortIDLength]
}

// applyContainers fills a deployment's status and per-service container details
// from its project's containers.
func applyContainers(deployment *models.Deployment, containers []container.Summary) {
	deployment.Status = statusFromContainers(containers)

	byService := make(map[string]container.Summary, len(containers))
	for _, c := range containers {
		service := c.Labels[composeServiceLabel]
		if service == "" {
			continue
		}
		if _, seen := byService[service]; !seen {
			byService[service] = c
		}
	}

	for i := range deployment.Services {
		svc := &deployment.Services[i]
		c, ok := byService[svc.Name]
		if !ok {
			continue
		}
		svc.ContainerID = shortContainerID(c.ID)
		svc.Status = c.State
		if health := healthFromStatus(c.Status); health != "" {
			svc.Health = health
		}
	}
}

type Manager struct {
	discovery      *Discovery
	executor       *ComposeExecutor
	apiClient      *APIClient
	basePath       string
	cleanupTimeout time.Duration
	mu             sync.RWMutex

	// apiDegraded tracks whether status reads are currently falling back to the
	// compose CLI, so the fallback is reported once per outage instead of once
	// per request.
	apiDegraded atomic.Bool
}

// noteStatusFallback reports that status reads have dropped to the compose CLI.
// The deployment list is polled continuously, so logging every failed request
// would bury the logs at the moment they are worth reading; only the change of
// state is logged.
func (m *Manager) noteStatusFallback(err error) {
	if m.apiDegraded.CompareAndSwap(false, true) {
		log.Printf("status: docker api unavailable, falling back to the compose cli: %v", err)
	}
}

// noteStatusRecovered reports that status reads are served by the engine again.
func (m *Manager) noteStatusRecovered() {
	if m.apiDegraded.CompareAndSwap(true, false) {
		log.Printf("status: docker api reachable again")
	}
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
	} else {
		// The client is built once and never rebuilt, so this degrades every
		// later status read to the compose CLI. Say so rather than leaving the
		// agent quietly slow with no way to tell why.
		log.Printf("docker api client unavailable, deployment status will use the slower compose cli: %v", err)
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

	ctx, cancel := context.WithTimeout(context.Background(), statusReadTimeout)
	defer cancel()

	index, err := m.indexContainersByProject(ctx)
	if err == nil {
		m.noteStatusRecovered()
		for i := range deployments {
			deployments[i].Status = statusFromContainers(index[m.projectFor(&deployments[i], index)])
		}
		return deployments, nil
	}
	m.noteStatusFallback(err)

	// Fallback only. Each status check shells out to docker compose, so a serial
	// loop makes list latency grow with the deployment count. Fetch them
	// concurrently with a bounded worker pool; each goroutine writes only its
	// own index.
	var wg sync.WaitGroup
	sem := make(chan struct{}, 12)
	for i := range deployments {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			status, _ := m.executor.GetStatus(deployments[i].Path)
			deployments[i].Status = status
		}(i)
	}
	wg.Wait()

	return deployments, nil
}

// StreamDeploymentLogs follows a deployment's logs until ctx is done, handing each line to
// sink as the container writes it.
//
// The deployment path is passed in rather than looked up again: the caller has already read
// the deployment, and following holds for as long as someone is watching, which is far too
// long to hold the manager's lock.
func (m *Manager) StreamDeploymentLogs(ctx context.Context, name, path string, tail int, sink func(string), services ...string) error {
	return m.executor.StreamLogs(ctx, path, tail, sink, services...)
}

// FindDeployments returns deployments built from their on-disk metadata alone,
// leaving Status unread. Callers that only need names, paths or metadata should
// prefer it over ListDeployments so they never pay for a Docker round-trip.
func (m *Manager) FindDeployments() ([]models.Deployment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.discovery.FindDeployments()
}

func (m *Manager) GetDeployment(name string) (*models.Deployment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	deployment, err := m.discovery.GetDeployment(name)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), statusReadTimeout)
	defer cancel()

	index, err := m.indexContainersByProject(ctx)
	if err == nil {
		m.noteStatusRecovered()
		applyContainers(deployment, index[m.projectFor(deployment, index)])
		return deployment, nil
	}
	m.noteStatusFallback(err)

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

func (m *Manager) CreateDeploymentFromSource(name, srcDir, composeContent, composeName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.discovery.CreateDeploymentFromSource(name, srcDir, composeContent, composeName)
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

func (m *Manager) StopDeployment(name string, opts ...RunOption) (string, error) {
	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return "", err
	}

	return m.executor.Stop(deployment.Path, opts...)
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

func (m *Manager) GetDeploymentLogs(name string, tail int, services ...string) (string, error) {
	m.mu.RLock()
	deployment, err := m.discovery.GetDeployment(name)
	m.mu.RUnlock()

	if err != nil {
		return "", err
	}

	return m.executor.Logs(deployment.Path, tail, services...)
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
