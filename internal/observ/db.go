package observ

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

// Resolutions the samples are kept at. Raw samples answer "what is happening now" at full
// detail; older ranges are read at a coarser step, because a day of 5-second samples is far
// more points than a chart can draw and far more rows than the history is worth.
const (
	rawWindow      = 6 * time.Hour
	rollupStep     = time.Minute
	rollupWindow   = 7 * 24 * time.Hour
	rollupInterval = 5 * time.Minute
)

// MetricsDB persists samples so history survives a restart and reaches past the in-memory
// window. The in-memory ring stays the hot path for the live view; this backs the longer
// ranges and anything asked for after a restart.
type MetricsDB struct {
	conn *sql.DB
	path string
}

// OpenMetricsDB opens the metrics database beside the other FlatRun state.
func OpenMetricsDB(dataDir string) (*MetricsDB, error) {
	dir := filepath.Join(dataDir, ".flatrun")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	path := filepath.Join(dir, "metrics.db")
	conn, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	// The writer is a single ticker and readers are few; one connection keeps SQLite out of
	// lock contention entirely.
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	conn.SetConnMaxLifetime(time.Hour)

	db := &MetricsDB{conn: conn, path: path}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, err
	}
	return db, nil
}

func (db *MetricsDB) Close() error { return db.conn.Close() }

func (db *MetricsDB) migrate() error {
	_, err := db.conn.Exec(`
		CREATE TABLE IF NOT EXISTS samples (
			deployment TEXT NOT NULL,
			container  TEXT NOT NULL,
			metric     TEXT NOT NULL,
			ts         INTEGER NOT NULL,
			value      REAL NOT NULL,
			step       INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_samples_lookup ON samples(deployment, container, metric, ts);
		CREATE INDEX IF NOT EXISTS idx_samples_ts ON samples(ts);
	`)
	return err
}

// WriteBatch stores a set of samples in one transaction, which is how the collector's tick
// arrives and keeps a per-row fsync off the sampling path.
func (db *MetricsDB) WriteBatch(points []LatestPoint) error {
	if len(points) == 0 {
		return nil
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`INSERT INTO samples (deployment, container, metric, ts, value, step) VALUES (?, ?, ?, ?, ?, 0)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, p := range points {
		if _, err := stmt.Exec(p.Deployment, p.Container, p.Metric, p.Time.UnixMilli(), p.Value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Range returns one series between two times, reading the resolution that suits the span.
func (db *MetricsDB) Range(key SeriesKey, since, until time.Time) ([]Sample, error) {
	rows, err := db.conn.Query(
		`SELECT ts, value FROM samples
		 WHERE deployment = ? AND container = ? AND metric = ? AND ts >= ? AND ts <= ?
		 ORDER BY ts`,
		key.Deployment, key.Container, key.Metric, since.UnixMilli(), until.UnixMilli(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Sample
	for rows.Next() {
		var ms int64
		var v float64
		if err := rows.Scan(&ms, &v); err != nil {
			return nil, err
		}
		out = append(out, Sample{Time: time.UnixMilli(ms), Value: v})
	}
	return out, rows.Err()
}

// Series lists the series the database holds within a window.
func (db *MetricsDB) Series(since time.Time) ([]SeriesKey, error) {
	rows, err := db.conn.Query(
		`SELECT DISTINCT deployment, container, metric FROM samples WHERE ts >= ?`,
		since.UnixMilli(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SeriesKey
	for rows.Next() {
		var k SeriesKey
		if err := rows.Scan(&k.Deployment, &k.Container, &k.Metric); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Deployment != out[j].Deployment {
			return out[i].Deployment < out[j].Deployment
		}
		if out[i].Container != out[j].Container {
			return out[i].Container < out[j].Container
		}
		return out[i].Metric < out[j].Metric
	})
	return out, nil
}

// storedSamples reads charts out of the database rather than the live window, which is what
// lets a range reach further back than memory holds and survive a restart.
type storedSamples struct {
	db    *MetricsDB
	since time.Time
	now   func() time.Time
}

func (s storedSamples) Series() []SeriesKey {
	keys, err := s.db.Series(s.since)
	if err != nil {
		return nil
	}
	return keys
}

func (s storedSamples) Range(key SeriesKey, since time.Time) []Sample {
	samples, err := s.db.Range(key, since, s.now())
	if err != nil {
		return nil
	}
	return samples
}

// Rollup folds raw samples older than the raw window into one averaged point per step, then
// deletes the raw rows it replaced. Without it a busy host writes every series every few
// seconds forever; with it, old history costs a fraction of that and still draws the same
// shape at the zoom levels it is read at.
//
// Averaging is the honest summary for these series: they are gauges, so a bucket's mean is
// what the container was doing over that minute.
func (db *MetricsDB) Rollup(now time.Time) error {
	cutoff := now.Add(-rawWindow).UnixMilli()
	step := rollupStep.Milliseconds()

	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// A bucket's timestamp is its start, so re-running this is idempotent: rolled-up rows
	// already carry step > 0 and are not folded again.
	if _, err := tx.Exec(`
		INSERT INTO samples (deployment, container, metric, ts, value, step)
		SELECT deployment, container, metric, (ts / ?) * ?, AVG(value), ?
		FROM samples
		WHERE step = 0 AND ts < ?
		GROUP BY deployment, container, metric, ts / ?
	`, step, step, step, cutoff, step); err != nil {
		return err
	}

	if _, err := tx.Exec(`DELETE FROM samples WHERE step = 0 AND ts < ?`, cutoff); err != nil {
		return err
	}
	return tx.Commit()
}

// Prune drops history past the retention window.
func (db *MetricsDB) Prune(now time.Time, retention time.Duration) error {
	if retention <= 0 {
		retention = rollupWindow
	}
	_, err := db.conn.Exec(`DELETE FROM samples WHERE ts < ?`, now.Add(-retention).UnixMilli())
	return err
}

// Maintain rolls up and prunes on a timer until ctx is done.
func (db *MetricsDB) Maintain(stop <-chan struct{}, retention time.Duration) {
	ticker := time.NewTicker(rollupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			if err := db.Rollup(now); err != nil {
				logMaintain("rollup", err)
			}
			if err := db.Prune(now, retention); err != nil {
				logMaintain("prune", err)
			}
		}
	}
}

func logMaintain(what string, err error) {
	fmt.Fprintf(os.Stderr, "observability: metrics %s failed: %v\n", what, err)
}
