package capacity

import (
	"testing"

	"github.com/flatrun/agent/pkg/config"
)

func TestDiagnoseIncreasesContainerMemoryBeforeScaling(t *testing.T) {
	policy := DefaultPolicy()
	host := Host{MemoryTotal: 16 << 30, MemoryAvailable: 8 << 30, CPUCores: 8, CPUUsagePercent: 20}
	container := Container{MemoryUsage: 950 << 20, MemoryLimit: 1 << 30}

	diagnosis := Diagnose(host, container, policy)
	if diagnosis.Pressure != PressureAllocation || diagnosis.Action != ActionIncreaseMemory {
		t.Fatalf("diagnosis = %#v", diagnosis)
	}
	if diagnosis.RecommendedLimit != float64(1536<<20) {
		t.Fatalf("recommended memory = %.0f", diagnosis.RecommendedLimit)
	}
}

func TestDiagnoseAddsReplicaWhenHostCannotResize(t *testing.T) {
	policy := DefaultPolicy()
	host := Host{MemoryTotal: 4 << 30, MemoryAvailable: 600 << 20, CPUCores: 2, CPUUsagePercent: 80}
	container := Container{MemoryUsage: 950 << 20, MemoryLimit: 1 << 30}

	diagnosis := Diagnose(host, container, policy)
	if diagnosis.Pressure != PressureHost || diagnosis.Action != ActionAddReplica {
		t.Fatalf("diagnosis = %#v", diagnosis)
	}
}

func TestDiagnoseReportsUnlimitedContainerAsHealthyWhenHostHasHeadroom(t *testing.T) {
	policy := DefaultPolicy()
	host := Host{MemoryTotal: 16 << 30, MemoryAvailable: 10 << 30, CPUCores: 8, CPUUsagePercent: 10}

	diagnosis := Diagnose(host, Container{MemoryUsage: 6 << 30, CPUPercent: 200}, policy)
	if diagnosis.Pressure != PressureNone || diagnosis.Action != ActionNone {
		t.Fatalf("diagnosis = %#v", diagnosis)
	}
}

func TestPolicyFromConfigPreservesExplicitScalingChoices(t *testing.T) {
	disabled := false
	policy := PolicyFromConfig(config.CapacityConfig{
		AllocationThresholdPercent: 75,
		HostThresholdPercent:       80,
		AllowVertical:              &disabled,
		AllowHorizontal:            &disabled,
	})

	if policy.AllocationThresholdPercent != 75 || policy.HostThresholdPercent != 80 {
		t.Fatalf("thresholds = %.0f, %.0f", policy.AllocationThresholdPercent, policy.HostThresholdPercent)
	}
	if policy.AllowVertical || policy.AllowHorizontal {
		t.Fatalf("scaling choices = vertical:%t horizontal:%t", policy.AllowVertical, policy.AllowHorizontal)
	}
	if policy.HostMemoryReserve == 0 || policy.MemoryStepPercent == 0 {
		t.Fatalf("defaults were not retained: %#v", policy)
	}
}

func TestFleetOfferKeepsHostReserve(t *testing.T) {
	policy := DefaultPolicy()
	offer := FleetOffer(Host{
		CPUCores:        8,
		CPUUsagePercent: 25,
		MemoryAvailable: 8 << 30,
	}, policy, true)

	if !offer.Enabled {
		t.Fatalf("offer = %#v", offer)
	}
	if offer.AvailableCPU != 5.75 {
		t.Fatalf("available CPU = %.2f", offer.AvailableCPU)
	}
	if offer.AvailableMemory != 7680<<20 {
		t.Fatalf("available memory = %d", offer.AvailableMemory)
	}
}

func TestFleetOfferRequiresOperatorPermission(t *testing.T) {
	offer := FleetOffer(Host{CPUCores: 8, MemoryAvailable: 8 << 30}, DefaultPolicy(), false)
	if offer.Enabled || offer.AvailableCPU != 0 || offer.AvailableMemory != 0 {
		t.Fatalf("offer = %#v", offer)
	}
}
