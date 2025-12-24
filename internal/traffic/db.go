package traffic

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

	dbPath := filepath.Join(dbDir, "traffic.db")
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
	CREATE TABLE IF NOT EXISTS traffic_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		deployment_name TEXT NOT NULL,
		request_path TEXT,
		request_method TEXT,
		status_code INTEGER,
		source_ip TEXT,
		response_time_ms INTEGER,
		bytes_sent INTEGER,
		request_length INTEGER,
		upstream_time_ms INTEGER,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_traffic_deployment ON traffic_logs(deployment_name);
	CREATE INDEX IF NOT EXISTS idx_traffic_created ON traffic_logs(created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_traffic_status ON traffic_logs(status_code);
	CREATE INDEX IF NOT EXISTS idx_traffic_source_ip ON traffic_logs(source_ip);
	`

	_, err := db.conn.Exec(schema)
	return err
}

func (db *DB) InsertLog(log *TrafficLog) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	result, err := db.conn.Exec(`
		INSERT INTO traffic_logs
		(deployment_name, request_path, request_method, status_code, source_ip, response_time_ms, bytes_sent, request_length, upstream_time_ms, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		log.DeploymentName, log.RequestPath, log.RequestMethod, log.StatusCode,
		log.SourceIP, log.ResponseTimeMs, log.BytesSent, log.RequestLength,
		log.UpstreamTimeMs, log.CreatedAt,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (db *DB) GetLogs(filter *TrafficFilter) ([]TrafficLog, int, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	query := "SELECT id, deployment_name, request_path, request_method, status_code, source_ip, response_time_ms, bytes_sent, request_length, upstream_time_ms, created_at FROM traffic_logs WHERE 1=1"
	countQuery := "SELECT COUNT(*) FROM traffic_logs WHERE 1=1"
	args := []interface{}{}

	if filter.DeploymentName != "" {
		query += " AND deployment_name = ?"
		countQuery += " AND deployment_name = ?"
		args = append(args, filter.DeploymentName)
	}
	if filter.RequestMethod != "" {
		query += " AND request_method = ?"
		countQuery += " AND request_method = ?"
		args = append(args, filter.RequestMethod)
	}
	if filter.StatusCode != nil {
		query += " AND status_code = ?"
		countQuery += " AND status_code = ?"
		args = append(args, *filter.StatusCode)
	}
	if filter.StatusGroup != "" {
		switch filter.StatusGroup {
		case "2xx":
			query += " AND status_code >= 200 AND status_code < 300"
			countQuery += " AND status_code >= 200 AND status_code < 300"
		case "3xx":
			query += " AND status_code >= 300 AND status_code < 400"
			countQuery += " AND status_code >= 300 AND status_code < 400"
		case "4xx":
			query += " AND status_code >= 400 AND status_code < 500"
			countQuery += " AND status_code >= 400 AND status_code < 500"
		case "5xx":
			query += " AND status_code >= 500"
			countQuery += " AND status_code >= 500"
		}
	}
	if filter.SourceIP != "" {
		query += " AND source_ip = ?"
		countQuery += " AND source_ip = ?"
		args = append(args, filter.SourceIP)
	}
	if filter.RequestPath != "" {
		query += " AND request_path LIKE ?"
		countQuery += " AND request_path LIKE ?"
		args = append(args, "%"+filter.RequestPath+"%")
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

	var logs []TrafficLog
	for rows.Next() {
		var log TrafficLog
		var upstreamTimeMs sql.NullInt64
		if err := rows.Scan(&log.ID, &log.DeploymentName, &log.RequestPath, &log.RequestMethod,
			&log.StatusCode, &log.SourceIP, &log.ResponseTimeMs, &log.BytesSent,
			&log.RequestLength, &upstreamTimeMs, &log.CreatedAt); err != nil {
			return nil, 0, err
		}
		if upstreamTimeMs.Valid {
			val := int(upstreamTimeMs.Int64)
			log.UpstreamTimeMs = &val
		}
		logs = append(logs, log)
	}

	return logs, total, nil
}

func (db *DB) GetStats(deploymentName string, since time.Duration) (*TrafficStats, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	stats := &TrafficStats{
		ByStatusGroup: make(map[string]int64),
		ByDeployment:  make(map[string]int64),
		ByMethod:      make(map[string]int64),
	}

	sinceTime := time.Now().Add(-since)
	deploymentFilter := ""
	args := []interface{}{sinceTime}

	if deploymentName != "" {
		deploymentFilter = " AND deployment_name = ?"
		args = append(args, deploymentName)
	}

	// Total requests and bytes
	var avgTime sql.NullFloat64
	err := db.conn.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(bytes_sent), 0), AVG(response_time_ms)
		FROM traffic_logs WHERE created_at >= ?`+deploymentFilter, args...).
		Scan(&stats.TotalRequests, &stats.TotalBytes, &avgTime)
	if err != nil {
		return nil, err
	}
	if avgTime.Valid {
		stats.AvgResponseTimeMs = avgTime.Float64
	}

	// By status group
	rows, err := db.conn.Query(`
		SELECT
			CASE
				WHEN status_code >= 200 AND status_code < 300 THEN '2xx'
				WHEN status_code >= 300 AND status_code < 400 THEN '3xx'
				WHEN status_code >= 400 AND status_code < 500 THEN '4xx'
				WHEN status_code >= 500 THEN '5xx'
				ELSE 'other'
			END as status_group,
			COUNT(*) as cnt
		FROM traffic_logs
		WHERE created_at >= ?`+deploymentFilter+`
		GROUP BY status_group`, args...)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var group string
			var count int64
			if err := rows.Scan(&group, &count); err == nil {
				stats.ByStatusGroup[group] = count
			}
		}
	}

	// By deployment
	rows, err = db.conn.Query(`
		SELECT deployment_name, COUNT(*) as cnt
		FROM traffic_logs
		WHERE created_at >= ?`+deploymentFilter+`
		GROUP BY deployment_name
		ORDER BY cnt DESC
		LIMIT 20`, args...)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var name string
			var count int64
			if err := rows.Scan(&name, &count); err == nil {
				stats.ByDeployment[name] = count
			}
		}
	}

	// By method
	rows, err = db.conn.Query(`
		SELECT request_method, COUNT(*) as cnt
		FROM traffic_logs
		WHERE created_at >= ?`+deploymentFilter+`
		GROUP BY request_method`, args...)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var method string
			var count int64
			if err := rows.Scan(&method, &count); err == nil {
				stats.ByMethod[method] = count
			}
		}
	}

	// Top paths with deployment context
	rows, err = db.conn.Query(`
		SELECT deployment_name, request_path, COUNT(*) as cnt, AVG(response_time_ms) as avg_time,
			SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END) as errors
		FROM traffic_logs
		WHERE created_at >= ?`+deploymentFilter+`
		GROUP BY deployment_name, request_path
		ORDER BY avg_time DESC
		LIMIT 10`, args...)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var p PathStats
			if err := rows.Scan(&p.Deployment, &p.Path, &p.RequestCount, &p.AvgTimeMs, &p.ErrorCount); err == nil {
				stats.TopPaths = append(stats.TopPaths, p)
			}
		}
	}

	// Top IPs
	rows, err = db.conn.Query(`
		SELECT source_ip, COUNT(*) as cnt, SUM(bytes_sent) as bytes, MAX(created_at) as last_seen
		FROM traffic_logs
		WHERE created_at >= ?`+deploymentFilter+`
		GROUP BY source_ip
		ORDER BY cnt DESC
		LIMIT 10`, args...)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var ip IPTrafficStats
			if err := rows.Scan(&ip.IP, &ip.RequestCount, &ip.BytesSent, &ip.LastSeen); err == nil {
				stats.TopIPs = append(stats.TopIPs, ip)
			}
		}
	}

	// Requests per hour (last 24 hours)
	rows, err = db.conn.Query(`
		SELECT strftime('%Y-%m-%d %H:00', created_at) as hour, COUNT(*) as cnt
		FROM traffic_logs
		WHERE created_at >= datetime('now', '-24 hours')`+deploymentFilter+`
		GROUP BY hour
		ORDER BY hour ASC`, args...)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var h HourlyStats
			if err := rows.Scan(&h.Hour, &h.RequestCount); err == nil {
				stats.RequestsPerHour = append(stats.RequestsPerHour, h)
			}
		}
	}

	// Deployment stats
	rows, err = db.conn.Query(`
		SELECT deployment_name, COUNT(*) as total,
			AVG(response_time_ms) as avg_time,
			SUM(CASE WHEN status_code >= 200 AND status_code < 300 THEN 1 ELSE 0 END) as s2xx,
			SUM(CASE WHEN status_code >= 300 AND status_code < 400 THEN 1 ELSE 0 END) as s3xx,
			SUM(CASE WHEN status_code >= 400 AND status_code < 500 THEN 1 ELSE 0 END) as s4xx,
			SUM(CASE WHEN status_code >= 500 THEN 1 ELSE 0 END) as s5xx
		FROM traffic_logs
		WHERE created_at >= ?`+deploymentFilter+`
		GROUP BY deployment_name
		ORDER BY total DESC`, args...)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var d DeploymentTrafficStats
			if err := rows.Scan(&d.Name, &d.TotalRequests, &d.AvgResponseTime,
				&d.Status2xx, &d.Status3xx, &d.Status4xx, &d.Status5xx); err == nil {
				if d.TotalRequests > 0 {
					d.ErrorRate = float64(d.Status4xx+d.Status5xx) / float64(d.TotalRequests) * 100
				}
				stats.DeploymentStats = append(stats.DeploymentStats, d)
			}
		}
	}

	return stats, nil
}

func (db *DB) Cleanup(olderThan time.Duration) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	cutoff := time.Now().Add(-olderThan)
	result, err := db.conn.Exec("DELETE FROM traffic_logs WHERE created_at < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (db *DB) GetUnknownDomainStats(knownDeployments []string, since time.Duration) (*UnknownDomainStats, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	stats := &UnknownDomainStats{
		TopDomains: []UnknownDomainEntry{},
		TopIPs:     []UnknownDomainIPEntry{},
		RecentLogs: []TrafficLog{},
	}

	cutoff := time.Now().Add(-since)

	placeholders := ""
	args := []interface{}{cutoff}
	for i, d := range knownDeployments {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, d)
	}

	notInClause := ""
	if len(knownDeployments) > 0 {
		notInClause = " AND deployment_name NOT IN (" + placeholders + ")"
	}

	// Total count
	var total int64
	err := db.conn.QueryRow(`
		SELECT COUNT(*) FROM traffic_logs
		WHERE created_at >= ?`+notInClause, args...).Scan(&total)
	if err != nil {
		return nil, err
	}
	stats.TotalRequests = total

	// Top domains
	rows, err := db.conn.Query(`
		SELECT deployment_name, COUNT(*) as cnt, MAX(created_at) as last_seen
		FROM traffic_logs
		WHERE created_at >= ?`+notInClause+`
		GROUP BY deployment_name
		ORDER BY cnt DESC
		LIMIT 20`, args...)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var entry UnknownDomainEntry
			if err := rows.Scan(&entry.Domain, &entry.RequestCount, &entry.LastSeen); err == nil {
				stats.TopDomains = append(stats.TopDomains, entry)
			}
		}
	}

	// Top IPs with domains they accessed
	rows, err = db.conn.Query(`
		SELECT source_ip, COUNT(*) as cnt,
			GROUP_CONCAT(DISTINCT deployment_name) as domains,
			MAX(created_at) as last_seen
		FROM traffic_logs
		WHERE created_at >= ?`+notInClause+`
		GROUP BY source_ip
		ORDER BY cnt DESC
		LIMIT 20`, args...)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var entry UnknownDomainIPEntry
			var domainsStr string
			if err := rows.Scan(&entry.IP, &entry.RequestCount, &domainsStr, &entry.LastSeen); err == nil {
				if domainsStr != "" {
					entry.Domains = append(entry.Domains, splitString(domainsStr, ",")...)
				}
				stats.TopIPs = append(stats.TopIPs, entry)
			}
		}
	}

	// Recent logs
	rows, err = db.conn.Query(`
		SELECT id, deployment_name, request_path, request_method, status_code,
			source_ip, response_time_ms, bytes_sent, request_length, upstream_time_ms, created_at
		FROM traffic_logs
		WHERE created_at >= ?`+notInClause+`
		ORDER BY created_at DESC
		LIMIT 50`, args...)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var log TrafficLog
			var upstreamTime sql.NullInt64
			if err := rows.Scan(&log.ID, &log.DeploymentName, &log.RequestPath,
				&log.RequestMethod, &log.StatusCode, &log.SourceIP,
				&log.ResponseTimeMs, &log.BytesSent, &log.RequestLength,
				&upstreamTime, &log.CreatedAt); err == nil {
				if upstreamTime.Valid {
					t := int(upstreamTime.Int64)
					log.UpstreamTimeMs = &t
				}
				stats.RecentLogs = append(stats.RecentLogs, log)
			}
		}
	}

	return stats, nil
}

func splitString(s, sep string) []string {
	if s == "" {
		return nil
	}
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep[0] {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	result = append(result, s[start:])
	return result
}
