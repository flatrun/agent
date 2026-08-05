package observ

import (
	"testing"
	"time"
)

func TestStoreRecordExpandsSemconvSeries(t *testing.T) {
	s := NewStore(10)
	t0 := time.Unix(1_700_000_000, 0)
	s.Record(ContainerSample{
		Deployment: "shop", Container: "shop-web",
		CPUPercent: 12.5, MemoryUsage: 100, MemoryLimit: 1000, NetworkRx: 5, NetworkTx: 7,
	}, t0)

	keys := s.Series()
	if len(keys) != 5 {
		t.Fatalf("expected 5 semconv series, got %d: %+v", len(keys), keys)
	}

	cpu := s.Range(SeriesKey{Deployment: "shop", Container: "shop-web", Metric: MetricCPUUsage}, t0)
	if len(cpu) != 1 || cpu[0].Value != 12.5 {
		t.Errorf("cpu series = %+v, want one sample of 12.5", cpu)
	}
	mem := s.Range(SeriesKey{Deployment: "shop", Container: "shop-web", Metric: MetricMemoryUsage}, t0)
	if len(mem) != 1 || mem[0].Value != 100 {
		t.Errorf("memory series = %+v, want one sample of 100", mem)
	}
}

func TestStoreNetworkCounterBecomesRate(t *testing.T) {
	s := NewStore(10)
	key := SeriesKey{Deployment: "shop", Container: "shop-web", Metric: MetricNetworkRx}
	t0 := time.Unix(1_700_000_000, 0)

	// First reading has nothing to diff against, so its rate is 0.
	s.Record(ContainerSample{Deployment: "shop", Container: "shop-web", NetworkRx: 1000}, t0)
	if got := lastValue(t, s, key); got != 0 {
		t.Errorf("first network rate = %v, want 0", got)
	}

	// 2000 more bytes over 5s is 400 B/s, regardless of how large the total is.
	s.Record(ContainerSample{Deployment: "shop", Container: "shop-web", NetworkRx: 3000}, t0.Add(5*time.Second))
	if got := lastValue(t, s, key); got != 400 {
		t.Errorf("network rate = %v, want 400", got)
	}

	// An idle container whose total does not move reports 0, not the huge total.
	s.Record(ContainerSample{Deployment: "shop", Container: "shop-web", NetworkRx: 3000}, t0.Add(10*time.Second))
	if got := lastValue(t, s, key); got != 0 {
		t.Errorf("idle network rate = %v, want 0", got)
	}

	// A counter reset (a restarted container reporting a smaller total) reports 0,
	// not a negative or spiked rate.
	s.Record(ContainerSample{Deployment: "shop", Container: "shop-web", NetworkRx: 100}, t0.Add(15*time.Second))
	if got := lastValue(t, s, key); got != 0 {
		t.Errorf("post-reset network rate = %v, want 0", got)
	}
}

func lastValue(t *testing.T, s *Store, key SeriesKey) float64 {
	t.Helper()
	samples := s.Range(key, time.Unix(0, 0))
	if len(samples) == 0 {
		t.Fatalf("no samples for %+v", key)
	}
	return samples[len(samples)-1].Value
}

func TestStoreRingEvictsOldest(t *testing.T) {
	s := NewStore(3)
	key := SeriesKey{Deployment: "d", Container: "c", Metric: MetricCPUUsage}
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < 5; i++ {
		s.Record(ContainerSample{Deployment: "d", Container: "c", CPUPercent: float64(i)}, base.Add(time.Duration(i)*time.Second))
	}

	got := s.Range(key, base)
	if len(got) != 3 {
		t.Fatalf("expected ring to hold 3, got %d", len(got))
	}
	// Oldest two (0,1) evicted; remaining are 2,3,4 in order.
	for i, want := range []float64{2, 3, 4} {
		if got[i].Value != want {
			t.Errorf("sample[%d] = %v, want %v", i, got[i].Value, want)
		}
	}
}

func TestStoreRangeSinceFilters(t *testing.T) {
	s := NewStore(10)
	key := SeriesKey{Deployment: "d", Container: "c", Metric: MetricCPUUsage}
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < 5; i++ {
		s.Record(ContainerSample{Deployment: "d", Container: "c", CPUPercent: float64(i)}, base.Add(time.Duration(i)*time.Second))
	}

	got := s.Range(key, base.Add(3*time.Second))
	if len(got) != 2 || got[0].Value != 3 || got[1].Value != 4 {
		t.Errorf("since(t+3s) = %+v, want samples 3 and 4", got)
	}
}

func TestStoreEvictsStaleSeries(t *testing.T) {
	s := NewStore(10)
	base := time.Unix(1_700_000_000, 0)
	s.Record(ContainerSample{Deployment: "gone", Container: "gone-web", CPUPercent: 1}, base)
	if len(s.Series()) != 5 {
		t.Fatalf("expected 5 series after first record, got %d", len(s.Series()))
	}

	// A live container reports well past the retention window; the departed one must be dropped.
	s.Record(ContainerSample{Deployment: "live", Container: "live-web", CPUPercent: 2}, base.Add(2*time.Hour))

	for _, k := range s.Series() {
		if k.Deployment == "gone" {
			t.Errorf("stale series was not evicted: %+v", k)
		}
	}
	if len(s.Series()) != 5 {
		t.Errorf("expected only the live deployment's 5 series to remain, got %d", len(s.Series()))
	}
}

func TestStoreRangeUnknownSeriesIsEmpty(t *testing.T) {
	s := NewStore(10)
	got := s.Range(SeriesKey{Deployment: "x", Container: "y", Metric: MetricCPUUsage}, time.Unix(0, 0))
	if got != nil {
		t.Errorf("unknown series should return nil, got %+v", got)
	}
}
