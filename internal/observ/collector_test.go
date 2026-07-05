package observ

import (
	"testing"
	"time"
)

func TestCollectorRecordsFromSource(t *testing.T) {
	store := NewStore(10)
	samples := []ContainerSample{
		{Deployment: "shop", Container: "shop-web", CPUPercent: 10, MemoryUsage: 200},
		{Deployment: "shop", Container: "shop-db", CPUPercent: 3, MemoryUsage: 500},
	}
	c := NewCollector(store, func() ([]ContainerSample, error) { return samples, nil }, time.Second)
	fixed := time.Unix(1_700_000_000, 0)
	c.now = func() time.Time { return fixed }

	if err := c.collectOnce(); err != nil {
		t.Fatalf("collectOnce: %v", err)
	}

	web := store.Range(SeriesKey{Deployment: "shop", Container: "shop-web", Metric: MetricCPUUsage}, fixed)
	if len(web) != 1 || web[0].Value != 10 {
		t.Errorf("web cpu = %+v, want one sample of 10", web)
	}
	// Two containers x five semconv metrics.
	if got := len(store.Series()); got != 10 {
		t.Errorf("expected 10 series, got %d", got)
	}
}

func TestParseBytes(t *testing.T) {
	cases := map[string]uint64{
		"0B":     0,
		"512B":   512,
		"1KiB":   1024,
		"1.5MiB": uint64(1.5 * (1 << 20)),
		"2GiB":   2 << 30,
		"100MB":  100 << 20,
		"":       0,
	}
	for in, want := range cases {
		if got := parseBytes(in); got != want {
			t.Errorf("parseBytes(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestSplitPair(t *testing.T) {
	a, b := splitPair("1.2MiB / 512MiB")
	if a != "1.2MiB" || b != "512MiB" {
		t.Errorf("splitPair = %q,%q", a, b)
	}
	if x, y := splitPair("garbage"); x != "" || y != "" {
		t.Errorf("splitPair(garbage) = %q,%q, want empty", x, y)
	}
}
