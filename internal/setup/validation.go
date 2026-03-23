package setup

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

type CheckResult struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Message  string `json:"message"`
	Required bool   `json:"required"`
}

func RunSystemChecks(deploymentsPath string) []CheckResult {
	return []CheckResult{
		CheckDocker(),
		CheckDockerCompose(),
		CheckDiskSpace(deploymentsPath),
		CheckMemory(),
		CheckPort(80, "FlatRun welcome page"),
		CheckPort(443, "FlatRun"),
	}
}

func CheckDocker() CheckResult {
	check := CheckResult{Name: "Docker", Required: true}
	if _, err := exec.LookPath("docker"); err != nil {
		check.Status = "fail"
		check.Message = "Docker is not installed"
		return check
	}
	out, err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").Output()
	if err != nil {
		check.Status = "fail"
		check.Message = "Docker is installed but not running"
	} else {
		check.Status = "pass"
		check.Message = fmt.Sprintf("Docker %s", strings.TrimSpace(string(out)))
	}
	return check
}

func CheckDockerCompose() CheckResult {
	check := CheckResult{Name: "Docker Compose", Required: true}
	out, err := exec.Command("docker", "compose", "version", "--short").Output()
	if err != nil {
		check.Status = "fail"
		check.Message = "Docker Compose plugin not found"
	} else {
		check.Status = "pass"
		check.Message = fmt.Sprintf("Docker Compose %s", strings.TrimSpace(string(out)))
	}
	return check
}

func CheckDiskSpace(path string) CheckResult {
	check := CheckResult{Name: "Disk Space", Required: true}
	diskFree := GetDiskFreeGB(path)
	if diskFree < 1 {
		check.Status = "fail"
		check.Message = "Less than 1 GB free disk space"
	} else if diskFree < 5 {
		check.Status = "warn"
		check.Message = fmt.Sprintf("%.1f GB free (5 GB+ recommended)", diskFree)
	} else {
		check.Status = "pass"
		check.Message = fmt.Sprintf("%.1f GB free", diskFree)
	}
	return check
}

func CheckMemory() CheckResult {
	check := CheckResult{Name: "Memory", Required: false}
	totalMB := GetHostMemoryMB()
	if totalMB < 512 {
		check.Status = "warn"
		check.Message = fmt.Sprintf("%d MB total (512 MB+ recommended)", totalMB)
	} else {
		check.Status = "pass"
		check.Message = fmt.Sprintf("%d MB total", totalMB)
	}
	return check
}

func CheckPort(port int, inUseBy string) CheckResult {
	check := CheckResult{
		Name:     fmt.Sprintf("Port %d", port),
		Required: false,
		Status:   "pass",
	}
	if IsPortAvailable(port) {
		check.Message = fmt.Sprintf("Port %d is available", port)
	} else {
		check.Message = fmt.Sprintf("Port %d is in use by %s", port, inUseBy)
	}
	return check
}

func IsPortAvailable(port int) bool {
	portStr := fmt.Sprintf("%d", port)
	for _, host := range []string{"0.0.0.0", "127.0.0.1"} {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, portStr))
		if err != nil {
			return false
		}
		ln.Close()
	}
	if ln, err := net.Listen("tcp6", net.JoinHostPort("::1", portStr)); err == nil {
		ln.Close()
	} else if isIPv6Supported() {
		return false
	}
	return true
}

func isIPv6Supported() bool {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

func GetDiskFreeGB(path string) float64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return -1
	}
	return float64(stat.Bavail*uint64(stat.Bsize)) / (1024 * 1024 * 1024)
}

func GetHostMemoryMB() uint64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return 0
			}
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0
			}
			return kb / 1024
		}
	}
	return 0
}
