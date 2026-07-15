package observ

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestTimeSeriesReachesPastTheLiveWindow is the bug this history exists for: the UI offers a
// 24h range while the in-memory window holds about an hour, so the longer ranges answered
// with whatever memory happened to have.
func TestTimeSeriesReachesPastTheLiveWindow(t *testing.T) {
	db := openTestDB(t)
	now := time.Now()

	// A tiny window, as if the process had only just started or had been restarted.
	store := NewStore(2)
	store.OnRecord(func(points []LatestPoint) {
		if err := db.WriteBatch(points); err != nil {
			t.Error(err)
		}
	})

	// Older than any live window would hold.
	if err := db.WriteBatch([]LatestPoint{
		{SeriesKey: key(MetricCPUUsage), Sample: Sample{Time: now.Add(-20 * time.Hour), Value: 11}},
		{SeriesKey: key(MetricCPUUsage), Sample: Sample{Time: now.Add(-10 * time.Hour), Value: 22}},
	}); err != nil {
		t.Fatal(err)
	}
	store.Record(ContainerSample{Deployment: "shop", Container: "shop-web", CPUPercent: 33}, now)

	rec := httptest.NewRecorder()
	Handler(store, db, nil, nil, nil).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/metrics/timeseries?deployment=shop&since=24h", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var body TimeSeriesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}

	series, ok := body.Metrics[MetricCPUUsage]
	if !ok {
		t.Fatalf("no cpu series in %v", body.Metrics)
	}
	// All three points: two from history, one just recorded. Without stored history the
	// ring of 2 could not have returned the 20h-old sample at all.
	if len(series.Timestamps) != 3 {
		t.Errorf("got %d points over 24h, want 3: %+v", len(series.Timestamps), series)
	}
}

// TestTimeSeriesFallsBackToMemoryWithoutHistory keeps the engine useful when the database
// could not be opened.
func TestTimeSeriesFallsBackToMemoryWithoutHistory(t *testing.T) {
	store := NewStore(10)
	store.Record(ContainerSample{Deployment: "shop", Container: "shop-web", CPUPercent: 7}, time.Now())

	rec := httptest.NewRecorder()
	Handler(store, nil, nil, nil, nil).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/metrics/timeseries?deployment=shop&since=15m", nil))

	var body TimeSeriesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body.Metrics[MetricCPUUsage]; !ok {
		t.Errorf("expected the live window to answer when there is no history: %+v", body.Metrics)
	}
}

// TestRecordPersistsWhatItStores pins the write-through: what the live window takes, history
// keeps.
func TestRecordPersistsWhatItStores(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(10)
	store.OnRecord(func(points []LatestPoint) {
		if err := db.WriteBatch(points); err != nil {
			t.Error(err)
		}
	})

	at := time.Now()
	store.Record(ContainerSample{Deployment: "shop", Container: "shop-web", CPUPercent: 5, MemoryUsage: 2048}, at)

	cpu, err := db.Range(key(MetricCPUUsage), at.Add(-time.Minute), at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(cpu) != 1 || cpu[0].Value != 5 {
		t.Errorf("cpu not persisted: %+v", cpu)
	}

	mem, err := db.Range(key(MetricMemoryUsage), at.Add(-time.Minute), at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(mem) != 1 || mem[0].Value != 2048 {
		t.Errorf("memory not persisted: %+v", mem)
	}
}
