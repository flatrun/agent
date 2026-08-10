package docker

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type ResourceLimits struct {
	MemoryLimit   int64   `json:"memory_limit"`
	MemorySwap    int64   `json:"memory_swap"`
	CPUs          float64 `json:"cpus"`
	CPUShares     int64   `json:"cpu_shares"`
	RestartPolicy string  `json:"restart_policy"`
}

type ResourceUpdate struct {
	MemoryLimit *int64   `json:"memory_limit,omitempty"`
	MemorySwap  *int64   `json:"memory_swap,omitempty"`
	CPUs        *float64 `json:"cpus,omitempty"`
	CPUShares   *int64   `json:"cpu_shares,omitempty"`
}

type hostConfig struct {
	Memory        int64                `json:"Memory"`
	MemorySwap    int64                `json:"MemorySwap"`
	NanoCpus      int64                `json:"NanoCpus"`
	CpuShares     int64                `json:"CpuShares"`
	RestartPolicy restartPolicyInspect `json:"RestartPolicy"`
}

type restartPolicyInspect struct {
	Name string `json:"Name"`
}

func GetContainerResources(containerID string) (*ResourceLimits, error) {
	cmd := exec.Command("docker", "inspect", "--format", "{{json .HostConfig}}", containerID)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container %s: %w", containerID, err)
	}

	var hc hostConfig
	if err := json.Unmarshal(output, &hc); err != nil {
		return nil, fmt.Errorf("failed to parse container config: %w", err)
	}

	return &ResourceLimits{
		MemoryLimit:   hc.Memory,
		MemorySwap:    hc.MemorySwap,
		CPUs:          float64(hc.NanoCpus) / 1e9,
		CPUShares:     hc.CpuShares,
		RestartPolicy: hc.RestartPolicy.Name,
	}, nil
}

func UpdateContainerResources(containerID string, update *ResourceUpdate) error {
	args := []string{"update"}

	if update.MemoryLimit != nil {
		args = append(args, "--memory", strconv.FormatInt(*update.MemoryLimit, 10))
	}
	if update.MemorySwap != nil {
		args = append(args, "--memory-swap", strconv.FormatInt(*update.MemorySwap, 10))
	}
	if update.CPUs != nil {
		args = append(args, "--cpus", strconv.FormatFloat(*update.CPUs, 'f', -1, 64))
	}
	if update.CPUShares != nil {
		args = append(args, "--cpu-shares", strconv.FormatInt(*update.CPUShares, 10))
	}

	args = append(args, containerID)

	cmd := exec.Command("docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker update failed: %s", strings.TrimSpace(string(output)))
	}

	return nil
}

func GetDeploymentResources(projectName string) (map[string]*ResourceLimits, error) {
	if projectName == "" {
		return nil, fmt.Errorf("project name is required")
	}

	psCmd := exec.Command("docker", "ps", "-q",
		"--filter", "label=com.docker.compose.project="+projectName)
	containerIDs, err := psCmd.Output()
	if err != nil || len(strings.TrimSpace(string(containerIDs))) == 0 {
		return map[string]*ResourceLimits{}, nil
	}

	ids := strings.Fields(strings.TrimSpace(string(containerIDs)))
	result := make(map[string]*ResourceLimits, len(ids))

	for _, id := range ids {
		nameCmd := exec.Command("docker", "inspect", "--format", "{{.Name}}", id)
		nameOutput, err := nameCmd.Output()
		name := strings.TrimPrefix(strings.TrimSpace(string(nameOutput)), "/")
		if err != nil || name == "" {
			name = id[:12]
		}

		limits, err := GetContainerResources(id)
		if err != nil {
			continue
		}
		result[name] = limits
	}

	return result, nil
}
