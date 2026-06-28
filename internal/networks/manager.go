package networks

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/flatrun/agent/pkg/models"
)

type Manager struct{}

func NewManager() *Manager {
	return &Manager{}
}

type dockerNetwork struct {
	ID         string                     `json:"Id"`
	Name       string                     `json:"Name"`
	Driver     string                     `json:"Driver"`
	Scope      string                     `json:"Scope"`
	IPAM       dockerIPAM                 `json:"IPAM"`
	Containers map[string]dockerContainer `json:"Containers"`
	Labels     map[string]string          `json:"Labels"`
	Created    string                     `json:"Created"`
}

type dockerIPAM struct {
	Driver string       `json:"Driver"`
	Config []ipamConfig `json:"Config"`
}

type ipamConfig struct {
	Subnet  string `json:"Subnet"`
	Gateway string `json:"Gateway"`
}

type dockerContainer struct {
	Name        string `json:"Name"`
	EndpointID  string `json:"EndpointID"`
	MacAddress  string `json:"MacAddress"`
	IPv4Address string `json:"IPv4Address"`
}

func (m *Manager) ListNetworks() ([]models.Network, error) {
	cmd := exec.Command("docker", "network", "ls", "--format", "{{.ID}}")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list networks: %w", err)
	}

	var ids []string
	for _, id := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if id != "" {
			ids = append(ids, id)
		}
	}

	// Inspect networks concurrently: a serial loop pays one docker call per
	// network. Order is preserved; networks that fail to inspect are dropped.
	results := make([]*models.Network, len(ids))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 12)
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if network, err := m.inspectNetwork(id); err == nil {
				results[i] = network
			}
		}(i, id)
	}
	wg.Wait()

	networks := make([]models.Network, 0, len(ids))
	for _, network := range results {
		if network != nil {
			networks = append(networks, *network)
		}
	}

	return networks, nil
}

func (m *Manager) inspectNetwork(id string) (*models.Network, error) {
	cmd := exec.Command("docker", "network", "inspect", id)
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var dockerNets []dockerNetwork
	if err := json.Unmarshal(output, &dockerNets); err != nil {
		return nil, err
	}

	if len(dockerNets) == 0 {
		return nil, fmt.Errorf("network not found")
	}

	dn := dockerNets[0]

	var containers []models.NetworkContainer
	for _, c := range dn.Containers {
		name := strings.TrimPrefix(c.Name, "/")
		containers = append(containers, models.NetworkContainer{
			Name:       name,
			IPv4:       c.IPv4Address,
			MacAddress: c.MacAddress,
		})
	}

	subnet := ""
	gateway := ""
	if len(dn.IPAM.Config) > 0 {
		subnet = dn.IPAM.Config[0].Subnet
		gateway = dn.IPAM.Config[0].Gateway
	}

	return &models.Network{
		ID:         dn.ID[:12],
		Name:       dn.Name,
		Driver:     dn.Driver,
		Scope:      dn.Scope,
		Subnet:     subnet,
		Gateway:    gateway,
		Containers: containers,
		Labels:     dn.Labels,
		Created:    dn.Created,
	}, nil
}

func (m *Manager) CreateNetwork(name, driver string, labels map[string]string) error {
	args := []string{"network", "create"}

	if driver != "" {
		args = append(args, "--driver", driver)
	}

	for k, v := range labels {
		args = append(args, "--label", fmt.Sprintf("%s=%s", k, v))
	}

	args = append(args, name)

	cmd := exec.Command("docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create network: %s", string(output))
	}

	return nil
}

func (m *Manager) DeleteNetwork(name string) error {
	cmd := exec.Command("docker", "network", "rm", name)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to delete network: %s", string(output))
	}
	return nil
}

func (m *Manager) ConnectContainer(networkName, containerName string) error {
	cmd := exec.Command("docker", "network", "connect", networkName, containerName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to connect container: %s", string(output))
	}
	return nil
}

func (m *Manager) DisconnectContainer(networkName, containerName string) error {
	cmd := exec.Command("docker", "network", "disconnect", networkName, containerName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to disconnect container: %s", string(output))
	}
	return nil
}

func (m *Manager) IsContainerOnNetwork(networkName, containerName string) bool {
	cmd := exec.Command("docker", "network", "inspect", networkName,
		"--format", "{{range .Containers}}{{.Name}} {{end}}")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	containers := strings.Fields(string(output))
	for _, c := range containers {
		if c == containerName {
			return true
		}
	}
	return false
}

func (m *Manager) EnsureContainerOnNetwork(networkName, containerName string) error {
	if containerName == "" {
		return nil
	}
	if m.IsContainerOnNetwork(networkName, containerName) {
		return nil
	}
	return m.ConnectContainer(networkName, containerName)
}

type ContainerInfo struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Image          string   `json:"image"`
	State          string   `json:"state"`
	Status         string   `json:"status"`
	Ports          []string `json:"ports"`
	Created        string   `json:"created"`
	DeploymentName string   `json:"deployment_name,omitempty"`
}

type ImageInfo struct {
	ID         string   `json:"id"`
	Tags       []string `json:"tags"`
	Size       int64    `json:"size"`
	Created    string   `json:"created"`
	Containers int      `json:"containers"`
}

type VolumeInfo struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Mountpoint string            `json:"mountpoint"`
	Created    string            `json:"created"`
	Size       int64             `json:"size"`
	InUse      bool              `json:"in_use"`
	Labels     map[string]string `json:"labels"`
	Containers []string          `json:"containers"`
}

func (m *Manager) GetContainerStats() (map[string]int, error) {
	stats := map[string]int{
		"total":   0,
		"running": 0,
		"stopped": 0,
	}

	cmd := exec.Command("docker", "ps", "-a", "--format", "{{.State}}")
	output, err := cmd.Output()
	if err != nil {
		return stats, err
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		stats["total"]++
		if line == "running" {
			stats["running"]++
		} else {
			stats["stopped"]++
		}
	}

	return stats, nil
}

func (m *Manager) GetImageStats() (map[string]int, error) {
	stats := map[string]int{
		"total": 0,
	}

	cmd := exec.Command("docker", "images", "-q")
	output, err := cmd.Output()
	if err != nil {
		return stats, err
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line != "" {
			stats["total"]++
		}
	}

	return stats, nil
}

func (m *Manager) GetVolumeStats() (map[string]int, error) {
	stats := map[string]int{
		"total": 0,
	}

	cmd := exec.Command("docker", "volume", "ls", "-q")
	output, err := cmd.Output()
	if err != nil {
		return stats, err
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line != "" {
			stats["total"]++
		}
	}

	return stats, nil
}

func (m *Manager) ListContainers() ([]ContainerInfo, error) {
	cmd := exec.Command("docker", "ps", "-a", "--format", "{{.ID}}|{{.Names}}|{{.Image}}|{{.State}}|{{.Status}}|{{.Ports}}|{{.CreatedAt}}|{{.Label \"com.docker.compose.project\"}}")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	var containers []ContainerInfo
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 8)
		if len(parts) < 7 {
			continue
		}

		ports := []string{}
		if parts[5] != "" {
			portList := strings.Split(parts[5], ", ")
			for _, p := range portList {
				if strings.Contains(p, "->") {
					ports = append(ports, p)
				}
			}
		}

		deploymentName := ""
		if len(parts) >= 8 {
			deploymentName = parts[7]
		}

		containers = append(containers, ContainerInfo{
			ID:             parts[0],
			Name:           parts[1],
			Image:          parts[2],
			State:          parts[3],
			Status:         parts[4],
			Ports:          ports,
			Created:        parts[6],
			DeploymentName: deploymentName,
		})
	}

	return containers, nil
}

func (m *Manager) StartContainer(id string) error {
	cmd := exec.Command("docker", "start", id)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to start container: %s", string(output))
	}
	return nil
}

func (m *Manager) StopContainer(id string) error {
	cmd := exec.Command("docker", "stop", id)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to stop container: %s", string(output))
	}
	return nil
}

func (m *Manager) RestartContainer(id string) error {
	cmd := exec.Command("docker", "restart", id)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to restart container: %s", string(output))
	}
	return nil
}

func (m *Manager) RemoveContainer(id string) error {
	cmd := exec.Command("docker", "rm", "-f", id)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to remove container: %s", string(output))
	}
	return nil
}

func (m *Manager) GetContainerLogs(id string, tail int) (string, error) {
	cmd := exec.Command("docker", "logs", "--tail", fmt.Sprintf("%d", tail), id)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get logs: %s", string(output))
	}
	return string(output), nil
}

func (m *Manager) ListImages() ([]ImageInfo, error) {
	cmd := exec.Command("docker", "images", "--format", "{{.ID}}|{{.Repository}}:{{.Tag}}|{{.Size}}|{{.CreatedAt}}")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w", err)
	}

	imageMap := make(map[string]*ImageInfo)
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}

		id := parts[0]
		tag := parts[1]
		sizeStr := parts[2]
		created := parts[3]

		size := parseSize(sizeStr)

		if existing, ok := imageMap[id]; ok {
			if tag != "<none>:<none>" {
				existing.Tags = append(existing.Tags, tag)
			}
		} else {
			tags := []string{}
			if tag != "<none>:<none>" {
				tags = append(tags, tag)
			}
			imageMap[id] = &ImageInfo{
				ID:         id,
				Tags:       tags,
				Size:       size,
				Created:    created,
				Containers: 0,
			}
		}
	}

	containerCmd := exec.Command("docker", "ps", "-a", "--format", "{{.Image}}")
	containerOutput, _ := containerCmd.Output()
	containerImages := strings.Split(strings.TrimSpace(string(containerOutput)), "\n")

	for _, img := range imageMap {
		for _, containerImg := range containerImages {
			for _, tag := range img.Tags {
				if strings.Contains(tag, containerImg) || strings.HasPrefix(img.ID, containerImg) {
					img.Containers++
				}
			}
		}
	}

	var images []ImageInfo
	for _, img := range imageMap {
		images = append(images, *img)
	}

	return images, nil
}

func parseSize(sizeStr string) int64 {
	sizeStr = strings.TrimSpace(sizeStr)
	var multiplier int64 = 1

	if strings.HasSuffix(sizeStr, "GB") {
		multiplier = 1024 * 1024 * 1024
		sizeStr = strings.TrimSuffix(sizeStr, "GB")
	} else if strings.HasSuffix(sizeStr, "MB") {
		multiplier = 1024 * 1024
		sizeStr = strings.TrimSuffix(sizeStr, "MB")
	} else if strings.HasSuffix(sizeStr, "KB") {
		multiplier = 1024
		sizeStr = strings.TrimSuffix(sizeStr, "KB")
	} else if strings.HasSuffix(sizeStr, "B") {
		sizeStr = strings.TrimSuffix(sizeStr, "B")
	}

	var size float64
	_, _ = fmt.Sscanf(strings.TrimSpace(sizeStr), "%f", &size)
	return int64(size * float64(multiplier))
}

func (m *Manager) RemoveImage(id string) error {
	cmd := exec.Command("docker", "rmi", id)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to remove image: %s", string(output))
	}
	return nil
}

func (m *Manager) PullImage(name string) error {
	cmd := exec.Command("docker", "pull", name)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to pull image: %s", string(output))
	}
	return nil
}

func (m *Manager) ListVolumes() ([]VolumeInfo, error) {
	cmd := exec.Command("docker", "volume", "ls", "--format", "{{.Name}}")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list volumes: %w", err)
	}

	var volumes []VolumeInfo
	names := strings.Split(strings.TrimSpace(string(output)), "\n")

	for _, name := range names {
		if name == "" {
			continue
		}

		inspectCmd := exec.Command("docker", "volume", "inspect", name)
		inspectOutput, err := inspectCmd.Output()
		if err != nil {
			continue
		}

		var volData []map[string]interface{}
		if err := json.Unmarshal(inspectOutput, &volData); err != nil {
			continue
		}

		if len(volData) == 0 {
			continue
		}

		vol := volData[0]
		labels := make(map[string]string)
		if labelsData, ok := vol["Labels"].(map[string]interface{}); ok {
			for k, v := range labelsData {
				labels[k] = fmt.Sprintf("%v", v)
			}
		}

		volume := VolumeInfo{
			Name:       name,
			Driver:     fmt.Sprintf("%v", vol["Driver"]),
			Mountpoint: fmt.Sprintf("%v", vol["Mountpoint"]),
			Created:    fmt.Sprintf("%v", vol["CreatedAt"]),
			Labels:     labels,
			InUse:      false,
			Containers: []string{},
		}

		containerCmd := exec.Command("docker", "ps", "-a", "--filter", fmt.Sprintf("volume=%s", name), "--format", "{{.Names}}")
		containerOutput, _ := containerCmd.Output()
		containerNames := strings.Split(strings.TrimSpace(string(containerOutput)), "\n")
		for _, cn := range containerNames {
			if cn != "" {
				volume.Containers = append(volume.Containers, cn)
				volume.InUse = true
			}
		}

		volumes = append(volumes, volume)
	}

	return volumes, nil
}

func (m *Manager) CreateVolume(name, driver string, labels map[string]string) error {
	args := []string{"volume", "create"}

	if driver != "" {
		args = append(args, "--driver", driver)
	}

	for k, v := range labels {
		args = append(args, "--label", fmt.Sprintf("%s=%s", k, v))
	}

	args = append(args, name)

	cmd := exec.Command("docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create volume: %s", string(output))
	}
	return nil
}

func (m *Manager) RemoveVolume(name string) error {
	cmd := exec.Command("docker", "volume", "rm", name)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to remove volume: %s", string(output))
	}
	return nil
}

func (m *Manager) PruneVolumes() (int, error) {
	cmd := exec.Command("docker", "volume", "prune", "-f")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("failed to prune volumes: %s", string(output))
	}

	count := 0
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "Deleted") || (len(line) > 0 && !strings.Contains(line, "Total") && !strings.Contains(line, "deleted")) {
			count++
		}
	}

	return count, nil
}

type Port struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Process  string `json:"process"`
	PID      int    `json:"pid"`
	Address  string `json:"address"`
	State    string `json:"state"`
}

func (m *Manager) ListPorts() ([]Port, error) {
	cmd := exec.Command("ss", "-tulpn", "state", "listening")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list ports: %w", err)
	}

	lines := strings.Split(string(output), "\n")
	var ports []Port

	for i, line := range lines {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		protocol := strings.ToUpper(fields[0])
		state := "LISTEN"
		localAddr := fields[3]

		addressParts := strings.Split(localAddr, ":")
		if len(addressParts) < 2 {
			continue
		}

		portStr := addressParts[len(addressParts)-1]
		port := 0
		_, _ = fmt.Sscanf(portStr, "%d", &port)
		if port == 0 {
			continue
		}

		address := strings.Join(addressParts[:len(addressParts)-1], ":")
		if address == "" || address == "*" {
			address = "0.0.0.0"
		}

		process := ""
		pid := 0

		if len(fields) >= 6 {
			processInfo := fields[5]
			if strings.Contains(processInfo, "pid=") {
				parts := strings.Split(processInfo, ",")
				for _, part := range parts {
					if strings.HasPrefix(part, "pid=") {
						_, _ = fmt.Sscanf(part, "pid=%d", &pid)
					}
				}

				if pid > 0 {
					procCmd := exec.Command("ps", "-p", fmt.Sprintf("%d", pid), "-o", "comm=")
					if procOutput, err := procCmd.Output(); err == nil {
						process = strings.TrimSpace(string(procOutput))
					}
				}
			}
		}

		ports = append(ports, Port{
			Port:     port,
			Protocol: protocol,
			Process:  process,
			PID:      pid,
			Address:  address,
			State:    state,
		})
	}

	return ports, nil
}

func (m *Manager) KillProcess(pid int) error {
	cmd := exec.Command("kill", "-9", fmt.Sprintf("%d", pid))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to kill process: %s", string(output))
	}
	return nil
}

func (m *Manager) EnsureNetwork(name string) error {
	if m.NetworkExists(name) {
		return nil
	}
	return m.CreateNetwork(name, "bridge", nil)
}

func (m *Manager) NetworkExists(name string) bool {
	cmd := exec.Command("docker", "network", "inspect", name)
	return cmd.Run() == nil
}
