package traffic

import (
	"log"
	"sync"
	"time"
)

type Manager struct {
	db            *DB
	retentionDays int
	mu            sync.RWMutex
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
