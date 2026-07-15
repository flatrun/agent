package traffic

import (
	"math"
	"sort"
	"time"
)

// REDPoint is one time bucket of a deployment's request behaviour: how many requests, how
// many failed, and how long they took.
type REDPoint struct {
	Time      time.Time `json:"time"`
	Requests  int64     `json:"requests"`
	Errors    int64     `json:"errors"`
	AvgTimeMs float64   `json:"avg_time_ms"`
	P95TimeMs float64   `json:"p95_time_ms"`
}

// ErrorRate is the share of requests that failed, as a percentage.
func (p REDPoint) ErrorRate() float64 {
	if p.Requests == 0 {
		return 0
	}
	return float64(p.Errors) / float64(p.Requests) * 100
}

// RequestsPerSecond is the rate over the bucket it was measured in.
func (p REDPoint) RequestsPerSecond(bucket time.Duration) float64 {
	if bucket <= 0 {
		return 0
	}
	return float64(p.Requests) / bucket.Seconds()
}

// REDSeries returns a deployment's request rate, error rate and latency over time, bucketed.
//
// Every deployment's traffic crosses FlatRun's proxy, and the proxy already reports each
// request here, so this is instrumentation of an application that was never asked to
// cooperate: no agent in the container, no change to the app.
//
// An empty deployment name covers every deployment at once.
func (db *DB) REDSeries(deploymentName string, since time.Time, bucket time.Duration) ([]REDPoint, error) {
	if bucket <= 0 {
		bucket = time.Minute
	}

	db.mu.RLock()
	defer db.mu.RUnlock()

	// The rows are filtered in SQL but bucketed here. created_at holds Go's own rendering of
	// a time, which SQLite's date functions cannot parse, while the driver reads the column
	// back as a real time: doing the arithmetic here is what keeps the buckets correct.
	// An exact p95 needs every value in a bucket anyway, so the rows have to be read.
	rows, err := db.conn.Query(`
		SELECT created_at, COALESCE(status_code, 0), COALESCE(response_time_ms, 0)
		FROM traffic_logs
		WHERE created_at >= ? AND (? = '' OR deployment_name = ?)
		ORDER BY created_at`,
		since, deploymentName, deploymentName,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type acc struct {
		requests  int64
		errors    int64
		totalMs   float64
		latencies []float64
	}
	buckets := map[int64]*acc{}

	for rows.Next() {
		var (
			at     time.Time
			status int
			ms     float64
		)
		if err := rows.Scan(&at, &status, &ms); err != nil {
			return nil, err
		}

		start := at.UTC().Truncate(bucket).Unix()
		b := buckets[start]
		if b == nil {
			b = &acc{}
			buckets[start] = b
		}
		b.requests++
		b.totalMs += ms
		b.latencies = append(b.latencies, ms)
		// Only a 5xx is the deployment failing. A 404 or a 401 is the request being wrong,
		// and counting those would make every crawler look like an outage.
		if status >= 500 {
			b.errors++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]REDPoint, 0, len(buckets))
	for start, b := range buckets {
		point := REDPoint{
			Time:      time.Unix(start, 0).UTC(),
			Requests:  b.requests,
			Errors:    b.errors,
			P95TimeMs: percentile(b.latencies, 0.95),
		}
		if b.requests > 0 {
			point.AvgTimeMs = b.totalMs / float64(b.requests)
		}
		out = append(out, point)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}

// percentile returns the nearest-rank percentile, which is the value below which that share
// of requests completed. An average hides exactly the slow requests a user notices.
func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append(make([]float64, 0, len(values)), values...)
	sort.Float64s(sorted)

	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}
