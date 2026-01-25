package audit

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	conn *sql.DB
	path string
	mu   sync.RWMutex
}

func NewDB(deploymentsPath string) (*DB, error) {
	dbDir := filepath.Join(deploymentsPath, ".flatrun")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, err
	}

	dbPath := filepath.Join(dbDir, "audit.db")
	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}

	conn.SetMaxOpenConns(10)
	conn.SetMaxIdleConns(5)
	conn.SetConnMaxLifetime(time.Hour)

	db := &DB{conn: conn, path: dbPath}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, err
	}

	return db, nil
}

func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.conn.Close()
}

func (db *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS audit_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id TEXT UNIQUE NOT NULL,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
		actor_type TEXT NOT NULL,
		actor_id TEXT,
		actor_name TEXT,
		api_key_prefix TEXT,
		action TEXT NOT NULL,
		method TEXT NOT NULL,
		path TEXT NOT NULL,
		resource_type TEXT,
		resource_id TEXT,
		client_ip TEXT NOT NULL,
		user_agent TEXT,
		request_id TEXT,
		request_body TEXT,
		response_status INTEGER,
		response_time_ms INTEGER,
		success BOOLEAN NOT NULL,
		error_message TEXT,
		metadata TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_events(timestamp DESC);
	CREATE INDEX IF NOT EXISTS idx_audit_actor ON audit_events(actor_id);
	CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_events(action);
	CREATE INDEX IF NOT EXISTS idx_audit_resource ON audit_events(resource_type, resource_id);
	CREATE INDEX IF NOT EXISTS idx_audit_success ON audit_events(success);
	CREATE INDEX IF NOT EXISTS idx_audit_client_ip ON audit_events(client_ip);
	CREATE INDEX IF NOT EXISTS idx_audit_api_key ON audit_events(api_key_prefix);
	`

	_, err := db.conn.Exec(schema)
	return err
}

func (db *DB) InsertEvent(event *AuditEvent) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	result, err := db.conn.Exec(`
		INSERT INTO audit_events
		(event_id, timestamp, actor_type, actor_id, actor_name, api_key_prefix,
		 action, method, path, resource_type, resource_id, client_ip, user_agent,
		 request_id, request_body, response_status, response_time_ms, success,
		 error_message, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EventID, event.Timestamp, event.ActorType, event.ActorID,
		event.ActorName, event.APIKeyPrefix, event.Action, event.Method,
		event.Path, event.ResourceType, event.ResourceID, event.ClientIP,
		event.UserAgent, event.RequestID, event.RequestBody, event.ResponseStatus,
		event.ResponseTimeMs, event.Success, event.ErrorMessage, event.Metadata,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (db *DB) GetEvents(filter *AuditFilter) ([]AuditEvent, int, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	query := `SELECT id, event_id, timestamp, actor_type, actor_id, actor_name,
		api_key_prefix, action, method, path, resource_type, resource_id,
		client_ip, user_agent, request_id, request_body, response_status,
		response_time_ms, success, error_message, metadata
		FROM audit_events WHERE 1=1`
	countQuery := "SELECT COUNT(*) FROM audit_events WHERE 1=1"
	args := []interface{}{}

	if filter.ActorID != "" {
		query += " AND actor_id = ?"
		countQuery += " AND actor_id = ?"
		args = append(args, filter.ActorID)
	}
	if filter.ActorType != "" {
		query += " AND actor_type = ?"
		countQuery += " AND actor_type = ?"
		args = append(args, filter.ActorType)
	}
	if filter.Action != "" {
		query += " AND action = ?"
		countQuery += " AND action = ?"
		args = append(args, filter.Action)
	}
	if filter.ResourceType != "" {
		query += " AND resource_type = ?"
		countQuery += " AND resource_type = ?"
		args = append(args, filter.ResourceType)
	}
	if filter.ResourceID != "" {
		query += " AND resource_id = ?"
		countQuery += " AND resource_id = ?"
		args = append(args, filter.ResourceID)
	}
	if filter.Success != nil {
		query += " AND success = ?"
		countQuery += " AND success = ?"
		args = append(args, *filter.Success)
	}
	if filter.ClientIP != "" {
		query += " AND client_ip = ?"
		countQuery += " AND client_ip = ?"
		args = append(args, filter.ClientIP)
	}
	if !filter.StartTime.IsZero() {
		query += " AND timestamp >= ?"
		countQuery += " AND timestamp >= ?"
		args = append(args, filter.StartTime)
	}
	if !filter.EndTime.IsZero() {
		query += " AND timestamp <= ?"
		countQuery += " AND timestamp <= ?"
		args = append(args, filter.EndTime)
	}

	var total int
	if err := db.conn.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query += " ORDER BY timestamp DESC"
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	if filter.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, filter.Offset)
	}

	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var events []AuditEvent
	for rows.Next() {
		var e AuditEvent
		var actorID, actorName, apiKeyPrefix, resourceType, resourceID sql.NullString
		var userAgent, requestID, requestBody, errorMsg, metadata sql.NullString
		var responseStatus, responseTimeMs sql.NullInt64

		if err := rows.Scan(
			&e.ID, &e.EventID, &e.Timestamp, &e.ActorType, &actorID, &actorName,
			&apiKeyPrefix, &e.Action, &e.Method, &e.Path, &resourceType, &resourceID,
			&e.ClientIP, &userAgent, &requestID, &requestBody, &responseStatus,
			&responseTimeMs, &e.Success, &errorMsg, &metadata,
		); err != nil {
			return nil, 0, err
		}

		e.ActorID = actorID.String
		e.ActorName = actorName.String
		e.APIKeyPrefix = apiKeyPrefix.String
		e.ResourceType = resourceType.String
		e.ResourceID = resourceID.String
		e.UserAgent = userAgent.String
		e.RequestID = requestID.String
		e.RequestBody = requestBody.String
		e.ErrorMessage = errorMsg.String
		e.Metadata = metadata.String
		if responseStatus.Valid {
			e.ResponseStatus = int(responseStatus.Int64)
		}
		if responseTimeMs.Valid {
			e.ResponseTimeMs = responseTimeMs.Int64
		}

		events = append(events, e)
	}

	return events, total, nil
}

func (db *DB) GetEventByID(id int64) (*AuditEvent, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var e AuditEvent
	var actorID, actorName, apiKeyPrefix, resourceType, resourceID sql.NullString
	var userAgent, requestID, requestBody, errorMsg, metadata sql.NullString
	var responseStatus, responseTimeMs sql.NullInt64

	err := db.conn.QueryRow(`
		SELECT id, event_id, timestamp, actor_type, actor_id, actor_name,
			api_key_prefix, action, method, path, resource_type, resource_id,
			client_ip, user_agent, request_id, request_body, response_status,
			response_time_ms, success, error_message, metadata
		FROM audit_events WHERE id = ?`, id).Scan(
		&e.ID, &e.EventID, &e.Timestamp, &e.ActorType, &actorID, &actorName,
		&apiKeyPrefix, &e.Action, &e.Method, &e.Path, &resourceType, &resourceID,
		&e.ClientIP, &userAgent, &requestID, &requestBody, &responseStatus,
		&responseTimeMs, &e.Success, &errorMsg, &metadata,
	)
	if err != nil {
		return nil, err
	}

	e.ActorID = actorID.String
	e.ActorName = actorName.String
	e.APIKeyPrefix = apiKeyPrefix.String
	e.ResourceType = resourceType.String
	e.ResourceID = resourceID.String
	e.UserAgent = userAgent.String
	e.RequestID = requestID.String
	e.RequestBody = requestBody.String
	e.ErrorMessage = errorMsg.String
	e.Metadata = metadata.String
	if responseStatus.Valid {
		e.ResponseStatus = int(responseStatus.Int64)
	}
	if responseTimeMs.Valid {
		e.ResponseTimeMs = responseTimeMs.Int64
	}

	return &e, nil
}

func (db *DB) GetEventByEventID(eventID string) (*AuditEvent, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var e AuditEvent
	var actorID, actorName, apiKeyPrefix, resourceType, resourceID sql.NullString
	var userAgent, requestID, requestBody, errorMsg, metadata sql.NullString
	var responseStatus, responseTimeMs sql.NullInt64

	err := db.conn.QueryRow(`
		SELECT id, event_id, timestamp, actor_type, actor_id, actor_name,
			api_key_prefix, action, method, path, resource_type, resource_id,
			client_ip, user_agent, request_id, request_body, response_status,
			response_time_ms, success, error_message, metadata
		FROM audit_events WHERE event_id = ?`, eventID).Scan(
		&e.ID, &e.EventID, &e.Timestamp, &e.ActorType, &actorID, &actorName,
		&apiKeyPrefix, &e.Action, &e.Method, &e.Path, &resourceType, &resourceID,
		&e.ClientIP, &userAgent, &requestID, &requestBody, &responseStatus,
		&responseTimeMs, &e.Success, &errorMsg, &metadata,
	)
	if err != nil {
		return nil, err
	}

	e.ActorID = actorID.String
	e.ActorName = actorName.String
	e.APIKeyPrefix = apiKeyPrefix.String
	e.ResourceType = resourceType.String
	e.ResourceID = resourceID.String
	e.UserAgent = userAgent.String
	e.RequestID = requestID.String
	e.RequestBody = requestBody.String
	e.ErrorMessage = errorMsg.String
	e.Metadata = metadata.String
	if responseStatus.Valid {
		e.ResponseStatus = int(responseStatus.Int64)
	}
	if responseTimeMs.Valid {
		e.ResponseTimeMs = responseTimeMs.Int64
	}

	return &e, nil
}

func (db *DB) GetStats() (*AuditStats, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	stats := &AuditStats{
		ByAction:       make(map[string]int),
		ByActorType:    make(map[string]int),
		ByResourceType: make(map[string]int),
	}

	_ = db.conn.QueryRow("SELECT COUNT(*) FROM audit_events").Scan(&stats.TotalEvents)
	_ = db.conn.QueryRow("SELECT COUNT(*) FROM audit_events WHERE timestamp >= datetime('now', '-24 hours')").Scan(&stats.Last24Hours)
	_ = db.conn.QueryRow("SELECT COUNT(*) FROM audit_events WHERE timestamp >= datetime('now', '-7 days')").Scan(&stats.Last7Days)
	_ = db.conn.QueryRow("SELECT COUNT(*) FROM audit_events WHERE success = 0").Scan(&stats.FailureCount)

	rows, err := db.conn.Query("SELECT action, COUNT(*) FROM audit_events GROUP BY action")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var action string
			var count int
			if err := rows.Scan(&action, &count); err == nil {
				stats.ByAction[action] = count
			}
		}
	}

	rows, err = db.conn.Query("SELECT actor_type, COUNT(*) FROM audit_events GROUP BY actor_type")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var actorType string
			var count int
			if err := rows.Scan(&actorType, &count); err == nil {
				stats.ByActorType[actorType] = count
			}
		}
	}

	rows, err = db.conn.Query("SELECT resource_type, COUNT(*) FROM audit_events WHERE resource_type IS NOT NULL AND resource_type != '' GROUP BY resource_type")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var resourceType string
			var count int
			if err := rows.Scan(&resourceType, &count); err == nil {
				stats.ByResourceType[resourceType] = count
			}
		}
	}

	rows, err = db.conn.Query(`
		SELECT actor_id, actor_type, COUNT(*) as cnt, MAX(timestamp) as last_seen
		FROM audit_events
		WHERE actor_id IS NOT NULL AND actor_id != ''
		GROUP BY actor_id, actor_type
		ORDER BY cnt DESC
		LIMIT 10`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var a ActorStats
			if err := rows.Scan(&a.ActorID, &a.ActorType, &a.EventCount, &a.LastSeen); err == nil {
				stats.TopActors = append(stats.TopActors, a)
			}
		}
	}

	rows, err = db.conn.Query(`
		SELECT date(timestamp) as dt, COUNT(*) as cnt
		FROM audit_events
		WHERE timestamp >= datetime('now', '-7 days')
		GROUP BY dt
		ORDER BY dt ASC`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var t TrendPoint
			if err := rows.Scan(&t.Date, &t.Count); err == nil {
				stats.EventsTrend = append(stats.EventsTrend, t)
			}
		}
	}

	return stats, nil
}

func (db *DB) CleanupOldEvents(olderThan time.Duration) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	cutoff := time.Now().Add(-olderThan)
	result, err := db.conn.Exec("DELETE FROM audit_events WHERE timestamp < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
