package api

import (
	"testing"

	"github.com/flatrun/agent/internal/capacity"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/orchestrator"
	"github.com/flatrun/agent/internal/system"
	"github.com/flatrun/agent/pkg/config"
)

func TestAutoscaleInputUsesMostConstrainedReplica(t *testing.T) {
	input := autoscaleInput([]autoscaleReplicaObservation{
		{Stats: docker.ContainerStats{ContainerID: "one", CPUPercent: 25, MemoryUsage: 100, MemoryPercent: 10}, Limits: docker.ResourceLimits{CPUs: 1, MemoryLimit: 1000}},
		{Stats: docker.ContainerStats{ContainerID: "two", CPUPercent: 95, MemoryUsage: 900, MemoryPercent: 90}, Limits: docker.ResourceLimits{CPUs: 1, MemoryLimit: 1000}},
	}, orchestrator.Status{Desired: 2}, &system.SystemStats{
		CPU:    system.CPUStats{Cores: 8, UsagePercent: 20},
		Memory: system.MemoryStats{Total: 16 << 30, Available: 8 << 30},
	}, config.CapacityConfig{})
	if input.Replicas != 2 || input.CPUPercent != 95 || input.MemoryPercent != 90 {
		t.Fatalf("input = %#v", input)
	}
	if input.Diagnosis.Action != capacity.ActionIncreaseMemory || input.SuggestedResource.MemoryLimit <= input.CurrentResources.MemoryLimit {
		t.Fatalf("diagnosis = %#v, input = %#v", input.Diagnosis, input)
	}
}

func TestUsesFleetPlacementOnlyForCapacityLabels(t *testing.T) {
	if !usesFleetPlacement(orchestrator.Placement{Constraints: []string{"node.labels.flatrun.capacity.a1b2==true"}}) {
		t.Fatal("Fleet capacity placement was not detected")
	}
	if usesFleetPlacement(orchestrator.Placement{Constraints: []string{"node.hostname==prod-1"}}) {
		t.Fatal("local placement was treated as Fleet capacity")
	}
}
