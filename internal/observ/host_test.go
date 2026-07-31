package observ

import (
	"testing"
	"time"
)

func TestHostSeriesRecorded(t *testing.T) {
	store := NewStore(10)
	store.RecordHost(HostSample{CPUPercent: 12, MemoryPercent: 40, MemoryUsage: 400, MemoryLimit: 1000, DiskPercent: 55}, time.Now())

	got := map[string]bool{}
	for _, p := range store.Latest() {
		if p.Container == HostContainer && p.Deployment == "" {
			got[p.Metric] = true
		}
	}
	for _, m := range []string{MetricHostCPU, MetricHostMemUtil, MetricHostMemUsage, MetricHostMemLimit, MetricHostDisk} {
		if !got[m] {
			t.Errorf("missing host metric %s", m)
		}
	}
}

func TestHostMetricsAreAlertable(t *testing.T) {
	for _, m := range []string{MetricHostCPU, MetricHostMemUtil, MetricHostMemUsage, MetricHostMemLimit, MetricHostDisk} {
		if !knownMetric(m) {
			t.Errorf("host metric %s should be alertable", m)
		}
	}
}

func TestHostAlertFires(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, store, fired := engineAt(t, &now)
	e.SetRules([]AlertRule{{
		ID: "h1", Name: "Host memory high", Metric: MetricHostMemUtil,
		Comparison: ComparisonAbove, Threshold: 90, ForSeconds: 0, Enabled: true,
	}})

	store.RecordHost(HostSample{MemoryPercent: 95, MemoryUsage: 950, MemoryLimit: 1000}, now)
	e.evaluate()

	if len(*fired) != 1 {
		t.Fatalf("expected a host memory alert, got %+v", *fired)
	}
	if (*fired)[0].Metric != MetricHostMemUtil {
		t.Errorf("wrong metric fired: %s", (*fired)[0].Metric)
	}
}
