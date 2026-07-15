package observ

import (
	"testing"
	"time"
)

func openTestDB(t *testing.T) *MetricsDB {
	t.Helper()
	db, err := OpenMetricsDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func key(metric string) SeriesKey {
	return SeriesKey{Deployment: "shop", Container: "shop-web", Metric: metric}
}

func TestMetricsDBWriteAndRange(t *testing.T) {
	db := openTestDB(t)
	at := time.Unix(1_700_000_000, 0)

	points := []LatestPoint{
		{SeriesKey: key(MetricCPUUsage), Sample: Sample{Time: at, Value: 10}},
		{SeriesKey: key(MetricCPUUsage), Sample: Sample{Time: at.Add(5 * time.Second), Value: 20}},
		{SeriesKey: key(MetricMemoryUsage), Sample: Sample{Time: at, Value: 1024}},
	}
	if err := db.WriteBatch(points); err != nil {
		t.Fatal(err)
	}

	got, err := db.Range(key(MetricCPUUsage), at.Add(-time.Minute), at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d samples, want 2: %+v", len(got), got)
	}
	if got[0].Value != 10 || got[1].Value != 20 {
		t.Errorf("samples out of order or wrong: %+v", got)
	}

	// A range is bounded at both ends: another metric's samples must not leak in.
	mem, err := db.Range(key(MetricMemoryUsage), at.Add(-time.Minute), at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(mem) != 1 || mem[0].Value != 1024 {
		t.Errorf("memory series = %+v", mem)
	}
}

func TestMetricsDBSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	at := time.Unix(1_700_000_000, 0)

	db, err := OpenMetricsDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WriteBatch([]LatestPoint{{SeriesKey: key(MetricCPUUsage), Sample: Sample{Time: at, Value: 42}}}); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// The point of persisting: a restart does not lose history.
	reopened, err := OpenMetricsDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	got, err := reopened.Range(key(MetricCPUUsage), at.Add(-time.Minute), at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Value != 42 {
		t.Errorf("history did not survive reopen: %+v", got)
	}
}

func TestMetricsDBRollupAveragesAndReplacesRawSamples(t *testing.T) {
	db := openTestDB(t)
	now := time.Unix(1_700_000_000, 0)
	// Aligned to a minute, so the samples below land in one bucket rather than being split
	// across two by the boundary.
	old := now.Add(-rawWindow - time.Hour).Truncate(time.Minute)

	// Twelve 5s samples inside that minute, averaging 27.5.
	var points []LatestPoint
	for i := 0; i < 12; i++ {
		points = append(points, LatestPoint{
			SeriesKey: key(MetricCPUUsage),
			Sample:    Sample{Time: old.Add(time.Duration(i) * 5 * time.Second), Value: float64(i * 5)},
		})
	}
	// One recent sample, which must be left at full resolution.
	points = append(points, LatestPoint{SeriesKey: key(MetricCPUUsage), Sample: Sample{Time: now, Value: 99}})
	if err := db.WriteBatch(points); err != nil {
		t.Fatal(err)
	}

	if err := db.Rollup(now); err != nil {
		t.Fatal(err)
	}

	rolled, err := db.Range(key(MetricCPUUsage), old.Add(-time.Hour), old.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rolled) != 1 {
		t.Fatalf("expected 12 raw samples folded into 1, got %d: %+v", len(rolled), rolled)
	}
	if rolled[0].Value != 27.5 {
		t.Errorf("rolled value = %v, want the bucket average 27.5", rolled[0].Value)
	}

	recent, err := db.Range(key(MetricCPUUsage), now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].Value != 99 {
		t.Errorf("recent samples must stay raw, got %+v", recent)
	}

	// Running again must not fold the rolled-up point a second time.
	if err := db.Rollup(now); err != nil {
		t.Fatal(err)
	}
	again, err := db.Range(key(MetricCPUUsage), old.Add(-time.Hour), old.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].Value != 27.5 {
		t.Errorf("rollup is not idempotent, got %+v", again)
	}
}

func TestMetricsDBPruneDropsOldHistory(t *testing.T) {
	db := openTestDB(t)
	now := time.Unix(1_700_000_000, 0)

	if err := db.WriteBatch([]LatestPoint{
		{SeriesKey: key(MetricCPUUsage), Sample: Sample{Time: now.Add(-48 * time.Hour), Value: 1}},
		{SeriesKey: key(MetricCPUUsage), Sample: Sample{Time: now, Value: 2}},
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.Prune(now, 24*time.Hour); err != nil {
		t.Fatal(err)
	}

	got, err := db.Range(key(MetricCPUUsage), now.Add(-72*time.Hour), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Value != 2 {
		t.Errorf("prune should keep only what is inside retention, got %+v", got)
	}
}

func TestMetricsDBSeries(t *testing.T) {
	db := openTestDB(t)
	at := time.Unix(1_700_000_000, 0)

	if err := db.WriteBatch([]LatestPoint{
		{SeriesKey: key(MetricCPUUsage), Sample: Sample{Time: at, Value: 1}},
		{SeriesKey: key(MetricCPUUsage), Sample: Sample{Time: at.Add(time.Second), Value: 2}},
		{SeriesKey: key(MetricMemoryUsage), Sample: Sample{Time: at, Value: 3}},
	}); err != nil {
		t.Fatal(err)
	}

	series, err := db.Series(at.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 2 {
		t.Fatalf("expected 2 distinct series, got %d: %+v", len(series), series)
	}
}
