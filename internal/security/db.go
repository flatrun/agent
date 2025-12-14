package security

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

	dbPath := filepath.Join(dbDir, "security.db")
	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}

	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
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
	CREATE TABLE IF NOT EXISTS security_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_type TEXT NOT NULL,
		severity TEXT NOT NULL CHECK (severity IN ('low', 'medium', 'high', 'critical')),
		source_ip TEXT NOT NULL,
		request_path TEXT,
		request_method TEXT,
		status_code INTEGER,
		user_agent TEXT,
		message TEXT NOT NULL,
		raw_log TEXT,
		deployment_name TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_events_created_at ON security_events(created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_events_source_ip ON security_events(source_ip);
	CREATE INDEX IF NOT EXISTS idx_events_severity ON security_events(severity);
	CREATE INDEX IF NOT EXISTS idx_events_deployment ON security_events(deployment_name);

	CREATE TABLE IF NOT EXISTS blocked_ips (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ip TEXT NOT NULL UNIQUE,
		reason TEXT,
		blocked_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		expires_at DATETIME,
		auto_blocked BOOLEAN DEFAULT FALSE
	);

	CREATE INDEX IF NOT EXISTS idx_blocked_ips_ip ON blocked_ips(ip);
	CREATE INDEX IF NOT EXISTS idx_blocked_ips_expires ON blocked_ips(expires_at);

	CREATE TABLE IF NOT EXISTS protected_routes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		path_pattern TEXT NOT NULL,
		rate_limit INTEGER DEFAULT 10,
		block_duration INTEGER DEFAULT 3600,
		enabled BOOLEAN DEFAULT TRUE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS log_processing_state (
		log_file TEXT PRIMARY KEY,
		last_position INTEGER DEFAULT 0,
		last_processed_at DATETIME
	);
	`

	_, err := db.conn.Exec(schema)
	return err
}

// InsertEvent inserts a new security event
func (db *DB) InsertEvent(event *SecurityEvent) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	result, err := db.conn.Exec(`
		INSERT INTO security_events
		(event_type, severity, source_ip, request_path, request_method, status_code, user_agent, message, raw_log, deployment_name, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EventType, event.Severity, event.SourceIP, event.RequestPath,
		event.RequestMethod, event.StatusCode, event.UserAgent, event.Message,
		event.RawLog, event.DeploymentName, event.CreatedAt,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetEvents retrieves events with optional filtering
func (db *DB) GetEvents(filter *EventFilter) ([]SecurityEvent, int, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	query := "SELECT id, event_type, severity, source_ip, request_path, request_method, status_code, user_agent, message, deployment_name, created_at FROM security_events WHERE 1=1"
	countQuery := "SELECT COUNT(*) FROM security_events WHERE 1=1"
	args := []interface{}{}

	if filter.EventType != "" {
		query += " AND event_type = ?"
		countQuery += " AND event_type = ?"
		args = append(args, filter.EventType)
	}
	if filter.Severity != "" {
		query += " AND severity = ?"
		countQuery += " AND severity = ?"
		args = append(args, filter.Severity)
	}
	if filter.SourceIP != "" {
		query += " AND source_ip = ?"
		countQuery += " AND source_ip = ?"
		args = append(args, filter.SourceIP)
	}
	if filter.DeploymentName != "" {
		query += " AND deployment_name = ?"
		countQuery += " AND deployment_name = ?"
		args = append(args, filter.DeploymentName)
	}
	if !filter.StartTime.IsZero() {
		query += " AND created_at >= ?"
		countQuery += " AND created_at >= ?"
		args = append(args, filter.StartTime)
	}
	if !filter.EndTime.IsZero() {
		query += " AND created_at <= ?"
		countQuery += " AND created_at <= ?"
		args = append(args, filter.EndTime)
	}

	var total int
	if err := db.conn.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query += " ORDER BY created_at DESC"
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

	var events []SecurityEvent
	for rows.Next() {
		var e SecurityEvent
		var requestPath, requestMethod, userAgent, deploymentName sql.NullString
		var statusCode sql.NullInt64
		if err := rows.Scan(&e.ID, &e.EventType, &e.Severity, &e.SourceIP, &requestPath, &requestMethod, &statusCode, &userAgent, &e.Message, &deploymentName, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		e.RequestPath = requestPath.String
		e.RequestMethod = requestMethod.String
		e.UserAgent = userAgent.String
		e.DeploymentName = deploymentName.String
		if statusCode.Valid {
			e.StatusCode = int(statusCode.Int64)
		}
		events = append(events, e)
	}

	return events, total, nil
}

// GetEventByID retrieves a single event by ID
func (db *DB) GetEventByID(id int64) (*SecurityEvent, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var e SecurityEvent
	var requestPath, requestMethod, userAgent, deploymentName sql.NullString
	var statusCode sql.NullInt64

	err := db.conn.QueryRow(`
		SELECT id, event_type, severity, source_ip, request_path, request_method, status_code, user_agent, message, deployment_name, created_at
		FROM security_events WHERE id = ?`, id).Scan(
		&e.ID, &e.EventType, &e.Severity, &e.SourceIP, &requestPath, &requestMethod,
		&statusCode, &userAgent, &e.Message, &deploymentName, &e.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	e.RequestPath = requestPath.String
	e.RequestMethod = requestMethod.String
	e.UserAgent = userAgent.String
	e.DeploymentName = deploymentName.String
	if statusCode.Valid {
		e.StatusCode = int(statusCode.Int64)
	}

	return &e, nil
}

// GetBlockedIPs retrieves all blocked IPs
func (db *DB) GetBlockedIPs() ([]BlockedIP, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	rows, err := db.conn.Query(`
		SELECT id, ip, reason, blocked_at, expires_at, auto_blocked
		FROM blocked_ips
		ORDER BY blocked_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ips []BlockedIP
	for rows.Next() {
		var b BlockedIP
		var reason sql.NullString
		var expiresAt sql.NullTime
		if err := rows.Scan(&b.ID, &b.IP, &reason, &b.BlockedAt, &expiresAt, &b.AutoBlocked); err != nil {
			return nil, err
		}
		b.Reason = reason.String
		if expiresAt.Valid {
			b.ExpiresAt = &expiresAt.Time
		}
		ips = append(ips, b)
	}

	return ips, nil
}

// GetActiveBlockedIPs retrieves IPs that are currently blocked (not expired)
func (db *DB) GetActiveBlockedIPs() ([]BlockedIP, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	rows, err := db.conn.Query(`
		SELECT id, ip, reason, blocked_at, expires_at, auto_blocked
		FROM blocked_ips
		WHERE expires_at IS NULL OR expires_at > datetime('now')
		ORDER BY blocked_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ips []BlockedIP
	for rows.Next() {
		var b BlockedIP
		var reason sql.NullString
		var expiresAt sql.NullTime
		if err := rows.Scan(&b.ID, &b.IP, &reason, &b.BlockedAt, &expiresAt, &b.AutoBlocked); err != nil {
			return nil, err
		}
		b.Reason = reason.String
		if expiresAt.Valid {
			b.ExpiresAt = &expiresAt.Time
		}
		ips = append(ips, b)
	}

	return ips, nil
}

// BlockIP adds an IP to the blocked list
func (db *DB) BlockIP(ip, reason string, expiresAt *time.Time, autoBlocked bool) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	result, err := db.conn.Exec(`
		INSERT INTO blocked_ips (ip, reason, expires_at, auto_blocked)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(ip) DO UPDATE SET reason = ?, expires_at = ?, auto_blocked = ?, blocked_at = CURRENT_TIMESTAMP`,
		ip, reason, expiresAt, autoBlocked, reason, expiresAt, autoBlocked,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// UnblockIP removes an IP from the blocked list
func (db *DB) UnblockIP(ip string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec("DELETE FROM blocked_ips WHERE ip = ?", ip)
	return err
}

// IsIPBlocked checks if an IP is currently blocked
func (db *DB) IsIPBlocked(ip string) (bool, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var count int
	err := db.conn.QueryRow(`
		SELECT COUNT(*) FROM blocked_ips
		WHERE ip = ? AND (expires_at IS NULL OR expires_at > datetime('now'))`,
		ip).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetProtectedRoutes retrieves all protected routes
func (db *DB) GetProtectedRoutes() ([]ProtectedRoute, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	rows, err := db.conn.Query(`
		SELECT id, path_pattern, rate_limit, block_duration, enabled, created_at
		FROM protected_routes
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var routes []ProtectedRoute
	for rows.Next() {
		var r ProtectedRoute
		if err := rows.Scan(&r.ID, &r.PathPattern, &r.RateLimit, &r.BlockDuration, &r.Enabled, &r.CreatedAt); err != nil {
			return nil, err
		}
		routes = append(routes, r)
	}

	return routes, nil
}

// GetEnabledProtectedRoutes retrieves only enabled protected routes
func (db *DB) GetEnabledProtectedRoutes() ([]ProtectedRoute, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	rows, err := db.conn.Query(`
		SELECT id, path_pattern, rate_limit, block_duration, enabled, created_at
		FROM protected_routes
		WHERE enabled = 1
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var routes []ProtectedRoute
	for rows.Next() {
		var r ProtectedRoute
		if err := rows.Scan(&r.ID, &r.PathPattern, &r.RateLimit, &r.BlockDuration, &r.Enabled, &r.CreatedAt); err != nil {
			return nil, err
		}
		routes = append(routes, r)
	}

	return routes, nil
}

// AddProtectedRoute adds a new protected route
func (db *DB) AddProtectedRoute(route *ProtectedRoute) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	result, err := db.conn.Exec(`
		INSERT INTO protected_routes (path_pattern, rate_limit, block_duration, enabled)
		VALUES (?, ?, ?, ?)`,
		route.PathPattern, route.RateLimit, route.BlockDuration, route.Enabled,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// UpdateProtectedRoute updates an existing protected route
func (db *DB) UpdateProtectedRoute(route *ProtectedRoute) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`
		UPDATE protected_routes
		SET path_pattern = ?, rate_limit = ?, block_duration = ?, enabled = ?
		WHERE id = ?`,
		route.PathPattern, route.RateLimit, route.BlockDuration, route.Enabled, route.ID,
	)
	return err
}

// DeleteProtectedRoute deletes a protected route
func (db *DB) DeleteProtectedRoute(id int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec("DELETE FROM protected_routes WHERE id = ?", id)
	return err
}

// GetStats retrieves security statistics
func (db *DB) GetStats() (*SecurityStats, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	stats := &SecurityStats{
		BySeverity: make(map[string]int),
		ByType:     make(map[string]int),
	}

	// Total events
	_ = db.conn.QueryRow("SELECT COUNT(*) FROM security_events").Scan(&stats.TotalEvents)

	// Last 24 hours
	_ = db.conn.QueryRow("SELECT COUNT(*) FROM security_events WHERE created_at >= datetime('now', '-24 hours')").Scan(&stats.Last24Hours)

	// Blocked IPs count
	_ = db.conn.QueryRow("SELECT COUNT(*) FROM blocked_ips WHERE expires_at IS NULL OR expires_at > datetime('now')").Scan(&stats.BlockedIPsCount)

	// Protected routes count
	_ = db.conn.QueryRow("SELECT COUNT(*) FROM protected_routes WHERE enabled = 1").Scan(&stats.ProtectedRoutesCount)

	// By severity
	rows, err := db.conn.Query("SELECT severity, COUNT(*) FROM security_events GROUP BY severity")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var severity string
			var count int
			if err := rows.Scan(&severity, &count); err == nil {
				stats.BySeverity[severity] = count
			}
		}
	}

	// By type
	rows, err = db.conn.Query("SELECT event_type, COUNT(*) FROM security_events GROUP BY event_type")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var eventType string
			var count int
			if err := rows.Scan(&eventType, &count); err == nil {
				stats.ByType[eventType] = count
			}
		}
	}

	// Top offending IPs
	rows, err = db.conn.Query(`
		SELECT source_ip, COUNT(*) as cnt, MAX(created_at) as last_seen
		FROM security_events
		GROUP BY source_ip
		ORDER BY cnt DESC
		LIMIT 10`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var ip IPStats
			if err := rows.Scan(&ip.IP, &ip.EventCount, &ip.LastSeen); err == nil {
				stats.TopOffendingIPs = append(stats.TopOffendingIPs, ip)
			}
		}
	}

	// Recent critical events
	rows, err = db.conn.Query(`
		SELECT id, event_type, severity, source_ip, request_path, message, created_at
		FROM security_events
		WHERE severity = 'critical'
		ORDER BY created_at DESC
		LIMIT 5`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var e SecurityEvent
			var requestPath sql.NullString
			if err := rows.Scan(&e.ID, &e.EventType, &e.Severity, &e.SourceIP, &requestPath, &e.Message, &e.CreatedAt); err == nil {
				e.RequestPath = requestPath.String
				stats.RecentCritical = append(stats.RecentCritical, e)
			}
		}
	}

	return stats, nil
}

// CleanupOldEvents deletes events older than the specified duration
func (db *DB) CleanupOldEvents(olderThan time.Duration) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	cutoff := time.Now().Add(-olderThan)
	result, err := db.conn.Exec("DELETE FROM security_events WHERE created_at < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// CleanupExpiredBlocks removes expired IP blocks
func (db *DB) CleanupExpiredBlocks() (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	result, err := db.conn.Exec("DELETE FROM blocked_ips WHERE expires_at IS NOT NULL AND expires_at <= datetime('now')")
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
