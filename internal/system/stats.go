package system

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type SystemStats struct {
	CPU     CPUStats     `json:"cpu"`
	Memory  MemoryStats  `json:"memory"`
	Disk    DiskStats    `json:"disk"`
	Uptime  int64        `json:"uptime"`
	LoadAvg LoadAverage  `json:"load_avg"`
}

type CPUStats struct {
	UsagePercent float64 `json:"usage_percent"`
	Cores        int     `json:"cores"`
}

type MemoryStats struct {
	Total        uint64  `json:"total"`
	Used         uint64  `json:"used"`
	Free         uint64  `json:"free"`
	Available    uint64  `json:"available"`
	UsagePercent float64 `json:"usage_percent"`
}

type DiskStats struct {
	Total        uint64  `json:"total"`
	Used         uint64  `json:"used"`
	Free         uint64  `json:"free"`
	UsagePercent float64 `json:"usage_percent"`
	MountPoint   string  `json:"mount_point"`
}

type LoadAverage struct {
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
}

var prevIdle, prevTotal uint64

func GetSystemStats() (*SystemStats, error) {
	stats := &SystemStats{}

	cpu, err := getCPUStats()
	if err == nil {
		stats.CPU = *cpu
	}

	mem, err := getMemoryStats()
	if err == nil {
		stats.Memory = *mem
	}

	disk, err := getDiskStats("/")
	if err == nil {
		stats.Disk = *disk
	}

	uptime, err := getUptime()
	if err == nil {
		stats.Uptime = uptime
	}

	load, err := getLoadAverage()
	if err == nil {
		stats.LoadAvg = *load
	}

	return stats, nil
}

func getCPUStats() (*CPUStats, error) {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "cpu ") {
			fields := strings.Fields(line)
			if len(fields) >= 5 {
				var total, idle uint64
				for i := 1; i < len(fields); i++ {
					val, _ := strconv.ParseUint(fields[i], 10, 64)
					total += val
					if i == 4 {
						idle = val
					}
				}

				var cpuPercent float64
				if prevTotal > 0 {
					totalDelta := total - prevTotal
					idleDelta := idle - prevIdle
					if totalDelta > 0 {
						cpuPercent = (1.0 - float64(idleDelta)/float64(totalDelta)) * 100
					}
				}

				prevTotal = total
				prevIdle = idle

				cores := getCPUCores()
				return &CPUStats{
					UsagePercent: cpuPercent,
					Cores:        cores,
				}, nil
			}
		}
	}

	return &CPUStats{}, nil
}

func getCPUCores() int {
	file, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return 1
	}
	defer file.Close()

	cores := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "processor") {
			cores++
		}
	}
	if cores == 0 {
		cores = 1
	}
	return cores
}

func getMemoryStats() (*MemoryStats, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	defer file.Close()

	stats := &MemoryStats{}
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		key := strings.TrimSuffix(fields[0], ":")
		value, _ := strconv.ParseUint(fields[1], 10, 64)
		value *= 1024

		switch key {
		case "MemTotal":
			stats.Total = value
		case "MemFree":
			stats.Free = value
		case "MemAvailable":
			stats.Available = value
		}
	}

	stats.Used = stats.Total - stats.Available
	if stats.Total > 0 {
		stats.UsagePercent = float64(stats.Used) / float64(stats.Total) * 100
	}

	return stats, nil
}

func getDiskStats(path string) (*DiskStats, error) {
	cmd := exec.Command("df", "-B1", path)
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(output), "\n")
	if len(lines) < 2 {
		return &DiskStats{MountPoint: path}, nil
	}

	fields := strings.Fields(lines[1])
	if len(fields) < 6 {
		return &DiskStats{MountPoint: path}, nil
	}

	total, _ := strconv.ParseUint(fields[1], 10, 64)
	used, _ := strconv.ParseUint(fields[2], 10, 64)
	free, _ := strconv.ParseUint(fields[3], 10, 64)

	var usagePercent float64
	if total > 0 {
		usagePercent = float64(used) / float64(total) * 100
	}

	return &DiskStats{
		Total:        total,
		Used:         used,
		Free:         free,
		UsagePercent: usagePercent,
		MountPoint:   path,
	}, nil
}

func getUptime() (int64, error) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}

	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return 0, nil
	}

	uptime, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, err
	}

	return int64(uptime), nil
}

func getLoadAverage() (*LoadAverage, error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil, err
	}

	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return &LoadAverage{}, nil
	}

	load1, _ := strconv.ParseFloat(fields[0], 64)
	load5, _ := strconv.ParseFloat(fields[1], 64)
	load15, _ := strconv.ParseFloat(fields[2], 64)

	return &LoadAverage{
		Load1:  load1,
		Load5:  load5,
		Load15: load15,
	}, nil
}

func init() {
	_, _ = getCPUStats()
	time.Sleep(100 * time.Millisecond)
}
