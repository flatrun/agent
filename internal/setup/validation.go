package setup

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/docker/docker/client"
)

type SystemCheck struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Message  string `json:"message"`
	Required bool   `json:"required"`
}

const (
	StatusPass = "pass"
	StatusFail = "fail"
	StatusWarn = "warn"
)

func (m *Manager) RunValidation() []SystemCheck {
	checks := []SystemCheck{}

	checks = append(checks, m.checkDocker())
	checks = append(checks, m.checkDockerSocket())
	checks = append(checks, m.checkDeploymentsDir())
	checks = append(checks, m.checkDiskSpace())
	checks = append(checks, m.checkMemory())
	checks = append(checks, m.checkNetwork())

	return checks
}

func (m *Manager) checkDocker() SystemCheck {
	check := SystemCheck{
		Name:     "Docker Daemon",
		Required: true,
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		check.Status = StatusFail
		check.Message = fmt.Sprintf("Failed to create Docker client: %v", err)
		return check
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = cli.Ping(ctx)
	if err != nil {
		check.Status = StatusFail
		check.Message = fmt.Sprintf("Docker daemon not responding: %v", err)
		return check
	}

	info, err := cli.Info(ctx)
	if err != nil {
		check.Status = StatusPass
		check.Message = "Docker daemon is running"
		return check
	}

	check.Status = StatusPass
	check.Message = fmt.Sprintf("Docker %s with %d containers", info.ServerVersion, info.Containers)
	return check
}

func (m *Manager) checkDockerSocket() SystemCheck {
	check := SystemCheck{
		Name:     "Docker Socket",
		Required: true,
	}

	socketPath := "/var/run/docker.sock"
	if m.config != nil && m.config.DockerSocket != "" {
		if len(m.config.DockerSocket) > 7 && m.config.DockerSocket[:7] == "unix://" {
			socketPath = m.config.DockerSocket[7:]
		}
	}

	info, err := os.Stat(socketPath)
	if err != nil {
		if os.IsNotExist(err) {
			check.Status = StatusFail
			check.Message = fmt.Sprintf("Docker socket not found at %s", socketPath)
		} else {
			check.Status = StatusFail
			check.Message = fmt.Sprintf("Cannot access Docker socket: %v", err)
		}
		return check
	}

	if info.Mode()&os.ModeSocket == 0 {
		check.Status = StatusFail
		check.Message = fmt.Sprintf("%s is not a socket", socketPath)
		return check
	}

	file, err := os.OpenFile(socketPath, os.O_RDWR, 0)
	if err != nil {
		check.Status = StatusFail
		check.Message = "No read/write permission on Docker socket"
		return check
	}
	file.Close()

	check.Status = StatusPass
	check.Message = "Docker socket accessible with proper permissions"
	return check
}

func (m *Manager) checkDeploymentsDir() SystemCheck {
	check := SystemCheck{
		Name:     "Deployments Directory",
		Required: true,
	}

	deploymentsPath := "/deployments"
	if m.config != nil && m.config.DeploymentsPath != "" {
		deploymentsPath = m.config.DeploymentsPath
	}

	info, err := os.Stat(deploymentsPath)
	if err != nil {
		if os.IsNotExist(err) {
			err := os.MkdirAll(deploymentsPath, 0755)
			if err != nil {
				check.Status = StatusFail
				check.Message = fmt.Sprintf("Cannot create deployments directory: %v", err)
				return check
			}
			check.Status = StatusPass
			check.Message = fmt.Sprintf("Created deployments directory at %s", deploymentsPath)
			return check
		}
		check.Status = StatusFail
		check.Message = fmt.Sprintf("Cannot access deployments directory: %v", err)
		return check
	}

	if !info.IsDir() {
		check.Status = StatusFail
		check.Message = fmt.Sprintf("%s exists but is not a directory", deploymentsPath)
		return check
	}

	testFile := deploymentsPath + "/.write_test"
	err = os.WriteFile(testFile, []byte("test"), 0644)
	if err != nil {
		check.Status = StatusFail
		check.Message = "Deployments directory is not writable"
		return check
	}
	os.Remove(testFile)

	check.Status = StatusPass
	check.Message = fmt.Sprintf("Deployments directory ready at %s", deploymentsPath)
	return check
}

func (m *Manager) checkDiskSpace() SystemCheck {
	check := SystemCheck{
		Name:     "Disk Space",
		Required: false,
	}

	deploymentsPath := "/deployments"
	if m.config != nil && m.config.DeploymentsPath != "" {
		deploymentsPath = m.config.DeploymentsPath
	}

	var stat syscall.Statfs_t
	err := syscall.Statfs(deploymentsPath, &stat)
	if err != nil {
		check.Status = StatusWarn
		check.Message = "Could not determine disk space"
		return check
	}

	availableGB := float64(stat.Bavail*uint64(stat.Bsize)) / (1024 * 1024 * 1024)
	totalGB := float64(stat.Blocks*uint64(stat.Bsize)) / (1024 * 1024 * 1024)
	usedPercent := (1 - float64(stat.Bavail)/float64(stat.Blocks)) * 100

	if availableGB < 1 {
		check.Status = StatusFail
		check.Message = fmt.Sprintf("Critically low disk space: %.1f GB available", availableGB)
	} else if availableGB < 5 {
		check.Status = StatusWarn
		check.Message = fmt.Sprintf("Low disk space: %.1f GB available (%.0f%% used of %.0f GB)", availableGB, usedPercent, totalGB)
	} else {
		check.Status = StatusPass
		check.Message = fmt.Sprintf("%.1f GB available (%.0f%% used of %.0f GB)", availableGB, usedPercent, totalGB)
	}

	return check
}

func (m *Manager) checkMemory() SystemCheck {
	check := SystemCheck{
		Name:     "System Memory",
		Required: false,
	}

	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		check.Status = StatusWarn
		check.Message = "Could not determine memory information"
		return check
	}

	var totalKB, availableKB uint64
	lines := string(data)
	for _, line := range splitLines(lines) {
		if len(line) > 9 && line[:9] == "MemTotal:" {
			fmt.Sscanf(line, "MemTotal: %d kB", &totalKB)
		}
		if len(line) > 13 && line[:13] == "MemAvailable:" {
			fmt.Sscanf(line, "MemAvailable: %d kB", &availableKB)
		}
	}

	if totalKB == 0 {
		check.Status = StatusWarn
		check.Message = "Could not parse memory information"
		return check
	}

	totalGB := float64(totalKB) / (1024 * 1024)
	availableGB := float64(availableKB) / (1024 * 1024)
	usedPercent := (1 - float64(availableKB)/float64(totalKB)) * 100

	if totalGB < 0.5 {
		check.Status = StatusFail
		check.Message = fmt.Sprintf("Insufficient memory: %.1f GB total", totalGB)
	} else if availableGB < 0.25 {
		check.Status = StatusWarn
		check.Message = fmt.Sprintf("Low available memory: %.2f GB free (%.0f%% used of %.1f GB)", availableGB, usedPercent, totalGB)
	} else {
		check.Status = StatusPass
		check.Message = fmt.Sprintf("%.2f GB available (%.0f%% used of %.1f GB)", availableGB, usedPercent, totalGB)
	}

	return check
}

func (m *Manager) checkNetwork() SystemCheck {
	check := SystemCheck{
		Name:     "Network Connectivity",
		Required: false,
	}

	conn, err := net.DialTimeout("tcp", "8.8.8.8:53", 5*time.Second)
	if err != nil {
		check.Status = StatusWarn
		check.Message = "Cannot reach external network"
		return check
	}
	conn.Close()

	_, err = net.LookupHost("registry.hub.docker.com")
	if err != nil {
		check.Status = StatusWarn
		check.Message = "DNS resolution not working"
		return check
	}

	check.Status = StatusPass
	check.Message = "Network connectivity OK"
	return check
}

func (m *Manager) CheckPortAvailable(port int) bool {
	addr := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

func (m *Manager) CheckCommandExists(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
