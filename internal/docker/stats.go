package docker

import (
	"encoding/json"
	"log"
	"os/exec"
	"strconv"
	"strings"
)

type ContainerStats struct {
	ContainerID    string  `json:"container_id"`
	Name           string  `json:"name"`
	DeploymentName string  `json:"deployment_name,omitempty"`
	CPUPercent     float64 `json:"cpu_percent"`
	MemoryUsage    uint64  `json:"memory_usage"`
	MemoryLimit    uint64  `json:"memory_limit"`
	MemoryPercent  float64 `json:"memory_percent"`
	NetworkRx      uint64  `json:"network_rx"`
	NetworkTx      uint64  `json:"network_tx"`
	BlockRead      uint64  `json:"block_read"`
	BlockWrite     uint64  `json:"block_write"`
	PIDs           int     `json:"pids"`
}

type dockerStatsJSON struct {
	Container string `json:"Container"`
	Name      string `json:"Name"`
	CPUPerc   string `json:"CPUPerc"`
	MemUsage  string `json:"MemUsage"`
	MemPerc   string `json:"MemPerc"`
	NetIO     string `json:"NetIO"`
	BlockIO   string `json:"BlockIO"`
	PIDs      string `json:"PIDs"`
}

func GetContainerStats(containerID string) (*ContainerStats, error) {
	cmd := exec.Command("docker", "stats", "--no-stream", "--format", "{{json .}}", containerID)
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var raw dockerStatsJSON
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, err
	}

	return parseStats(&raw), nil
}

func GetAllContainerStats() ([]ContainerStats, error) {
	deployments := listContainerDeploymentLabels()
	cmd := exec.Command("docker", "stats", "--no-stream", "--format", "{{json .}}")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var stats []ContainerStats
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		var raw dockerStatsJSON
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		stat := parseStats(&raw)
		if deploymentName := deployments[stat.ContainerID]; deploymentName != "" {
			stat.DeploymentName = deploymentName
		} else if deploymentName := deployments[stat.Name]; deploymentName != "" {
			stat.DeploymentName = deploymentName
		}
		stats = append(stats, *stat)
	}

	return stats, nil
}

func listContainerDeploymentLabels() map[string]string {
	labels := make(map[string]string)
	cmd := exec.Command("docker", "ps", "-a", "--format", "{{.ID}}|{{.Names}}|{{.Label \"com.docker.compose.project\"}}")
	output, err := cmd.Output()
	if err != nil {
		log.Printf("warning: failed to list container deployment labels: %v", err)
		return labels
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 || parts[2] == "" {
			continue
		}
		labels[parts[0]] = parts[2]
		labels[parts[1]] = parts[2]
	}
	return labels
}

func GetDeploymentStats(projectName string) ([]ContainerStats, error) {
	if projectName == "" {
		return []ContainerStats{}, nil
	}

	// Get container names by docker-compose project label
	psCmd := exec.Command("docker", "ps", "-q",
		"--filter", "label=com.docker.compose.project="+projectName)
	containerIDs, err := psCmd.Output()
	if err != nil || len(strings.TrimSpace(string(containerIDs))) == 0 {
		return []ContainerStats{}, nil
	}

	ids := strings.Fields(strings.TrimSpace(string(containerIDs)))
	if len(ids) == 0 {
		return []ContainerStats{}, nil
	}

	args := append([]string{"stats", "--no-stream", "--format", "{{json .}}"}, ids...)
	cmd := exec.Command("docker", args...)
	output, err := cmd.Output()
	if err != nil {
		return []ContainerStats{}, nil
	}

	var stats []ContainerStats
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		var raw dockerStatsJSON
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		stats = append(stats, *parseStats(&raw))
	}

	return stats, nil
}

func parseStats(raw *dockerStatsJSON) *ContainerStats {
	stats := &ContainerStats{
		ContainerID: raw.Container,
		Name:        raw.Name,
	}

	stats.CPUPercent = parsePercent(raw.CPUPerc)
	stats.MemoryPercent = parsePercent(raw.MemPerc)

	memParts := strings.Split(raw.MemUsage, " / ")
	if len(memParts) == 2 {
		stats.MemoryUsage = parseBytes(strings.TrimSpace(memParts[0]))
		stats.MemoryLimit = parseBytes(strings.TrimSpace(memParts[1]))
	}

	netParts := strings.Split(raw.NetIO, " / ")
	if len(netParts) == 2 {
		stats.NetworkRx = parseBytes(strings.TrimSpace(netParts[0]))
		stats.NetworkTx = parseBytes(strings.TrimSpace(netParts[1]))
	}

	blockParts := strings.Split(raw.BlockIO, " / ")
	if len(blockParts) == 2 {
		stats.BlockRead = parseBytes(strings.TrimSpace(blockParts[0]))
		stats.BlockWrite = parseBytes(strings.TrimSpace(blockParts[1]))
	}

	stats.PIDs, _ = strconv.Atoi(raw.PIDs)

	return stats
}

func parsePercent(s string) float64 {
	s = strings.TrimSuffix(s, "%")
	val, _ := strconv.ParseFloat(s, 64)
	return val
}

func parseBytes(s string) uint64 {
	s = strings.ToUpper(s)
	multiplier := uint64(1)

	if strings.HasSuffix(s, "KIB") || strings.HasSuffix(s, "KB") {
		multiplier = 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "KIB"), "KB")
	} else if strings.HasSuffix(s, "MIB") || strings.HasSuffix(s, "MB") {
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "MIB"), "MB")
	} else if strings.HasSuffix(s, "GIB") || strings.HasSuffix(s, "GB") {
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "GIB"), "GB")
	} else if strings.HasSuffix(s, "B") {
		s = strings.TrimSuffix(s, "B")
	}

	val, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return uint64(val * float64(multiplier))
}
