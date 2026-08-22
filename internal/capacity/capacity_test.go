package capacity

import "testing"

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
