package observ

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// StatsSource returns the current per-container readings. It is injectable so the collector
// can be tested without Docker.
type StatsSource func() ([]ContainerSample, error)

// Collector periodically reads a StatsSource and records the readings into a Store.
type Collector struct {
	store    *Store
	source   StatsSource
	interval time.Duration
	now      func() time.Time
}

func NewCollector(store *Store, source StatsSource, interval time.Duration) *Collector {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Collector{store: store, source: source, interval: interval, now: time.Now}
}

// collectOnce reads the source once and records the readings. Exposed for tests.
func (c *Collector) collectOnce() error {
	samples, err := c.source()
	if err != nil {
		return err
	}
	t := c.now()
	for _, s := range samples {
		c.store.Record(s, t)
	}
	return nil
}

// Run collects on each tick until ctx is cancelled. A failing read is skipped, not fatal,
// so a transient Docker hiccup does not stop collection.
func (c *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	_ = c.collectOnce()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = c.collectOnce()
		}
	}
}

// DockerStatsSource reads a point-in-time snapshot via `docker stats`, tagging each
// container with its compose project (deployment) from container labels.
func DockerStatsSource() ([]ContainerSample, error) {
	deployments := containerDeployments()

	out, err := exec.Command("docker", "stats", "--no-stream", "--format", "{{json .}}").Output()
	if err != nil {
		return nil, err
	}

	var samples []ContainerSample
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var raw struct {
			Container string `json:"Container"`
			Name      string `json:"Name"`
			CPUPerc   string `json:"CPUPerc"`
			MemUsage  string `json:"MemUsage"`
			NetIO     string `json:"NetIO"`
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		rx, tx := splitPair(raw.NetIO)
		used, limit := splitPair(raw.MemUsage)
		deployment := deployments[raw.Container]
		if deployment == "" {
			deployment = deployments[raw.Name]
		}
		samples = append(samples, ContainerSample{
			Deployment:  deployment,
			Container:   raw.Name,
			CPUPercent:  parsePercent(raw.CPUPerc),
			MemoryUsage: parseBytes(used),
			MemoryLimit: parseBytes(limit),
			NetworkRx:   parseBytes(rx),
			NetworkTx:   parseBytes(tx),
		})
	}
	return samples, nil
}

func containerDeployments() map[string]string {
	m := make(map[string]string)
	out, err := exec.Command("docker", "ps", "-a", "--format",
		"{{.ID}}|{{.Names}}|{{.Label \"com.docker.compose.project\"}}").Output()
	if err != nil {
		return m
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 || parts[2] == "" {
			continue
		}
		m[parts[0]] = parts[2]
		m[parts[1]] = parts[2]
	}
	return m
}

func splitPair(s string) (string, string) {
	parts := strings.Split(s, " / ")
	if len(parts) != 2 {
		return "", ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func parsePercent(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
	return v
}

func parseBytes(s string) uint64 {
	s = strings.ToUpper(strings.TrimSpace(s))
	mult := uint64(1)
	switch {
	case strings.HasSuffix(s, "KIB") || strings.HasSuffix(s, "KB"):
		mult = 1 << 10
		s = strings.TrimSuffix(strings.TrimSuffix(s, "KIB"), "KB")
	case strings.HasSuffix(s, "MIB") || strings.HasSuffix(s, "MB"):
		mult = 1 << 20
		s = strings.TrimSuffix(strings.TrimSuffix(s, "MIB"), "MB")
	case strings.HasSuffix(s, "GIB") || strings.HasSuffix(s, "GB"):
		mult = 1 << 30
		s = strings.TrimSuffix(strings.TrimSuffix(s, "GIB"), "GB")
	case strings.HasSuffix(s, "B"):
		s = strings.TrimSuffix(s, "B")
	}
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return uint64(v * float64(mult))
}
