package observ

import (
	"context"
	"time"

	"github.com/flatrun/agent/internal/system"
)

// HostSource returns the current host-wide reading. Injectable so the collector
// is testable without reading /proc.
type HostSource func() (HostSample, error)

// HostCollector periodically records a host reading into the store, giving the
// system-wide metrics the same time-series treatment as per-container ones.
type HostCollector struct {
	store    *Store
	source   HostSource
	interval time.Duration
	now      func() time.Time
}

func NewHostCollector(store *Store, source HostSource, interval time.Duration) *HostCollector {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &HostCollector{store: store, source: source, interval: interval, now: time.Now}
}

func (c *HostCollector) collectOnce() error {
	sample, err := c.source()
	if err != nil {
		return err
	}
	c.store.RecordHost(sample, c.now())
	return nil
}

// Run collects on each tick until ctx is cancelled. A failing read is skipped,
// not fatal, so a transient error does not stop host collection.
func (c *HostCollector) Run(ctx context.Context) {
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

// SystemHostSource reads host CPU, memory and disk from the system package. Its
// memory figure is working-set (total minus available), which is the number that
// tells whether the machine is actually under memory pressure.
func SystemHostSource() (HostSample, error) {
	stats, err := system.GetSystemStats()
	if err != nil {
		return HostSample{}, err
	}
	return HostSample{
		CPUPercent:    stats.CPU.UsagePercent,
		MemoryUsage:   stats.Memory.Used,
		MemoryLimit:   stats.Memory.Total,
		MemoryPercent: stats.Memory.UsagePercent,
		DiskPercent:   stats.Disk.UsagePercent,
	}, nil
}
