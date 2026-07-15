package traffic

import (
	"testing"
	"time"
)

func redDB(t *testing.T) *DB {
	t.Helper()
	db, err := NewDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// insert writes through the same path the proxy's ingest does. Writing rows in a format of
// the test's own choosing would prove only that the query matches the test.
func insert(t *testing.T, db *DB, deployment string, status, ms int, at time.Time) {
	t.Helper()
	_, err := db.InsertLog(&TrafficLog{
		DeploymentName: deployment,
		RequestPath:    "/",
		RequestMethod:  "GET",
		StatusCode:     status,
		SourceIP:       "10.0.0.1",
		ResponseTimeMs: ms,
		BytesSent:      100,
		RequestLength:  50,
		CreatedAt:      at,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestREDSeriesBucketsRequestsErrorsAndLatency(t *testing.T) {
	db := redDB(t)
	base := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Minute)

	// Ten requests in one minute: one server error, latency 10ms..100ms.
	for i := 1; i <= 10; i++ {
		status := 200
		if i == 10 {
			status = 503
		}
		insert(t, db, "shop", status, i*10, base.Add(time.Duration(i)*time.Second))
	}
	// A later bucket, so they must not be merged.
	insert(t, db, "shop", 200, 5, base.Add(2*time.Minute))

	points, err := db.REDSeries("shop", base.Add(-time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 {
		t.Fatalf("got %d buckets, want 2: %+v", len(points), points)
	}

	first := points[0]
	if first.Requests != 10 {
		t.Errorf("requests = %d, want 10", first.Requests)
	}
	// Only 5xx counts as the deployment failing; a 404 is the client's business.
	if first.Errors != 1 {
		t.Errorf("errors = %d, want 1", first.Errors)
	}
	if first.AvgTimeMs != 55 {
		t.Errorf("avg = %v, want 55", first.AvgTimeMs)
	}
	// Ten ranked values, so the nearest-rank 95th percentile is the 10th: 100ms.
	if first.P95TimeMs != 100 {
		t.Errorf("p95 = %v, want 100", first.P95TimeMs)
	}

	if points[1].Requests != 1 {
		t.Errorf("second bucket = %+v, want 1 request", points[1])
	}
}

func TestREDSeriesCountsOnlyServerErrors(t *testing.T) {
	db := redDB(t)
	at := time.Now().UTC().Add(-time.Minute)

	insert(t, db, "shop", 200, 10, at)
	insert(t, db, "shop", 404, 10, at)
	insert(t, db, "shop", 499, 10, at)
	insert(t, db, "shop", 500, 10, at)
	insert(t, db, "shop", 502, 10, at)

	points, err := db.REDSeries("shop", at.Add(-time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) == 0 {
		t.Fatal("no buckets")
	}
	if points[0].Errors != 2 {
		t.Errorf("errors = %d, want 2: a 404 is not the deployment failing", points[0].Errors)
	}
	if got := points[0].ErrorRate(); got != 40 {
		t.Errorf("error rate = %v, want 40", got)
	}
}

func TestREDSeriesScopedToDeployment(t *testing.T) {
	db := redDB(t)
	at := time.Now().UTC().Add(-time.Minute)

	insert(t, db, "shop", 200, 10, at)
	insert(t, db, "blog", 200, 10, at)

	points, err := db.REDSeries("shop", at.Add(-time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].Requests != 1 {
		t.Errorf("another deployment's traffic leaked in: %+v", points)
	}

	// No name means the whole host.
	all, err := db.REDSeries("", at.Add(-time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Requests != 2 {
		t.Errorf("unscoped series = %+v, want both requests", all)
	}
}

func TestREDSeriesEmpty(t *testing.T) {
	db := redDB(t)
	points, err := db.REDSeries("nothing", time.Now().Add(-time.Hour), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 0 {
		t.Errorf("expected no buckets, got %+v", points)
	}
}

func TestREDPointRates(t *testing.T) {
	p := REDPoint{Requests: 120, Errors: 6}
	if got := p.RequestsPerSecond(time.Minute); got != 2 {
		t.Errorf("rps = %v, want 2", got)
	}
	if got := p.ErrorRate(); got != 5 {
		t.Errorf("error rate = %v, want 5", got)
	}

	var empty REDPoint
	if empty.ErrorRate() != 0 || empty.RequestsPerSecond(time.Minute) != 0 {
		t.Error("an empty bucket must not divide by zero")
	}
}
