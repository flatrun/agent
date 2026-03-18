package docker

import (
	"testing"
)

func TestResourceUpdateArgs(t *testing.T) {
	mem := int64(512 * 1024 * 1024) // 512MB
	cpus := 1.5
	shares := int64(1024)

	update := &ResourceUpdate{
		MemoryLimit: &mem,
		CPUs:        &cpus,
		CPUShares:   &shares,
	}

	if update.MemoryLimit == nil || *update.MemoryLimit != mem {
		t.Errorf("MemoryLimit = %v, want %d", update.MemoryLimit, mem)
	}
	if update.CPUs == nil || *update.CPUs != cpus {
		t.Errorf("CPUs = %v, want %f", update.CPUs, cpus)
	}
	if update.CPUShares == nil || *update.CPUShares != shares {
		t.Errorf("CPUShares = %v, want %d", update.CPUShares, shares)
	}
	if update.MemorySwap != nil {
		t.Error("MemorySwap should be nil when not set")
	}
}

func TestResourceLimitsStruct(t *testing.T) {
	limits := &ResourceLimits{
		MemoryLimit:   536870912,
		MemorySwap:    -1,
		CPUs:          2.0,
		CPUShares:     1024,
		RestartPolicy: "always",
	}

	if limits.MemoryLimit != 536870912 {
		t.Errorf("MemoryLimit = %d, want 536870912", limits.MemoryLimit)
	}
	if limits.CPUs != 2.0 {
		t.Errorf("CPUs = %f, want 2.0", limits.CPUs)
	}
}
