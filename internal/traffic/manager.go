package traffic

import (
	"log"
	"time"
)

type Manager struct {
	db            *DB
	retentionDays int
}

func NewManager(deploymentsPath string, retentionDays int) (*Manager, error) {
	db, err := NewDB(deploymentsPath)
	if err != nil {
		return nil, err
	}

	if retentionDays <= 0 {
		retentionDays = 7
	}

	m := &Manager{
		db:            db,
		retentionDays: retentionDays,
	}

	go m.cleanupLoop()

	return m, nil
}

func (m *Manager) Close() error {
	return m.db.Close()
}

func (m *Manager) IngestLog(ingest *IngestTrafficLog) (*TrafficLog, error) {
	log := &TrafficLog{
		DeploymentName: ingest.DeploymentName,
		RequestPath:    ingest.RequestPath,
		RequestMethod:  ingest.RequestMethod,
		StatusCode:     ingest.StatusCode,
		SourceIP:       ingest.SourceIP,
		ResponseTimeMs: ingest.ResponseTimeMs,
		BytesSent:      ingest.BytesSent,
		RequestLength:  ingest.RequestLength,
		UpstreamTimeMs: ingest.UpstreamTimeMs,
		CreatedAt:      time.Now(),
	}

	if ingest.Timestamp > 0 {
		log.CreatedAt = time.Unix(ingest.Timestamp, 0)
	}

	id, err := m.db.InsertLog(log)
	if err != nil {
		return nil, err
	}
	log.ID = id

	return log, nil
}

func (m *Manager) GetLogs(filter *TrafficFilter) ([]TrafficLog, int, error) {
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	return m.db.GetLogs(filter)
}

func (m *Manager) GetStats(deploymentName string, since time.Duration) (*TrafficStats, error) {
	if since <= 0 {
		since = 24 * time.Hour
	}
	return m.db.GetStats(deploymentName, since)
}

// GetREDSeries returns a deployment's request rate, error rate and latency over time. The
// bucket is chosen from the window so a chart gets a useful number of points rather than one
// per second over a day.
func (m *Manager) GetREDSeries(deploymentName string, since time.Duration) ([]REDPoint, error) {
	if since <= 0 {
		since = time.Hour
	}
	return m.db.REDSeries(deploymentName, time.Now().Add(-since), bucketFor(since))
}

// bucketFor keeps a series around a hundred points whatever the window, which is about what a
// chart can draw before points stop being distinguishable.
func bucketFor(window time.Duration) time.Duration {
	switch {
	case window <= 15*time.Minute:
		return 10 * time.Second
	case window <= time.Hour:
		return time.Minute
	case window <= 6*time.Hour:
		return 5 * time.Minute
	case window <= 24*time.Hour:
		return 15 * time.Minute
	default:
		return time.Hour
	}
}

func (m *Manager) GetUnknownDomainStats(knownDeployments []string, since time.Duration) (*UnknownDomainStats, error) {
	if since <= 0 {
		since = 24 * time.Hour
	}
	return m.db.GetUnknownDomainStats(knownDeployments, since)
}

func (m *Manager) Cleanup(days int) (int64, error) {
	if days <= 0 {
		days = m.retentionDays
	}
	return m.db.Cleanup(time.Duration(days) * 24 * time.Hour)
}

func (m *Manager) cleanupLoop() {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		deleted, err := m.Cleanup(m.retentionDays)
		if err != nil {
			log.Printf("Traffic cleanup error: %v", err)
		} else if deleted > 0 {
			log.Printf("Traffic cleanup: deleted %d old logs", deleted)
		}
	}
}
