package observ

import (
	"testing"
	"time"
)

func TestBuildTimeSeriesAlignsContainers(t *testing.T) {
	store := NewStore(100)
	base := time.Unix(1_700_000_000, 0)
	// Two containers sampled at the same three ticks.
	for i := 0; i < 3; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		store.Record(ContainerSample{Deployment: "shop", Container: "shop-web", CPUPercent: float64(i)}, at)
		store.Record(ContainerSample{Deployment: "shop", Container: "shop-db", CPUPercent: float64(i * 2)}, at)
	}

	ms := buildTimeSeries(store, "shop", base.Add(-time.Minute))
	cpu, ok := ms[MetricCPUUsage]
	if !ok {
		t.Fatalf("cpu series missing; got metrics %v", keys(ms))
	}
	if len(cpu.Containers) != 2 {
		t.Fatalf("expected 2 containers, got %v", cpu.Containers)
	}
	if len(cpu.Timestamps) != 3 {
		t.Fatalf("expected 3 aligned timestamps, got %d", len(cpu.Timestamps))
	}
	// Values are [container][timestamp], aligned to the shared axis.
	web := cpu.Containers[0] // "shop-db" sorts first
	_ = web
	for ci, name := range cpu.Containers {
		if len(cpu.Values[ci]) != len(cpu.Timestamps) {
			t.Errorf("container %s row length %d != timestamps %d", name, len(cpu.Values[ci]), len(cpu.Timestamps))
		}
	}
}

func TestBuildTimeSeriesFiltersDeployment(t *testing.T) {
	store := NewStore(100)
	now := time.Unix(1_700_000_000, 0)
	store.Record(ContainerSample{Deployment: "shop", Container: "shop-web", CPUPercent: 1}, now)
	store.Record(ContainerSample{Deployment: "blog", Container: "blog-web", CPUPercent: 2}, now)

	ms := buildTimeSeries(store, "shop", now.Add(-time.Minute))
	for _, s := range ms {
		for _, c := range s.Containers {
			if c == "blog-web" {
				t.Errorf("blog container leaked into shop series")
			}
		}
	}
}

func keys(m map[string]MetricSeries) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
