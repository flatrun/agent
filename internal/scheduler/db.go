package scheduler

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
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

	dbPath := filepath.Join(dbDir, "scheduler.db")
	conn, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
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
	CREATE TABLE IF NOT EXISTS scheduled_tasks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		type TEXT NOT NULL CHECK (type IN ('backup', 'command')),
		deployment_name TEXT NOT NULL,
		cron_expr TEXT NOT NULL,
		enabled BOOLEAN DEFAULT TRUE,
		config TEXT,
		last_run DATETIME,
		next_run DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_tasks_deployment ON scheduled_tasks(deployment_name);
	CREATE INDEX IF NOT EXISTS idx_tasks_enabled ON scheduled_tasks(enabled);
	CREATE INDEX IF NOT EXISTS idx_tasks_next_run ON scheduled_tasks(next_run);

	CREATE TABLE IF NOT EXISTS task_executions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id INTEGER NOT NULL,
		status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'completed', 'failed')),
		output TEXT,
		error TEXT,
		started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		ended_at DATETIME,
		duration_ms INTEGER,
		FOREIGN KEY (task_id) REFERENCES scheduled_tasks(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_executions_task ON task_executions(task_id);
	CREATE INDEX IF NOT EXISTS idx_executions_started ON task_executions(started_at DESC);
	`

	_, err := db.conn.Exec(schema)
	return err
}

func (db *DB) CreateTask(task *ScheduledTask) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	configJSON, err := json.Marshal(task.Config)
	if err != nil {
		return 0, err
	}

	result, err := db.conn.Exec(`
		INSERT INTO scheduled_tasks (name, type, deployment_name, cron_expr, enabled, config, next_run)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		task.Name, task.Type, task.DeploymentName, task.CronExpr, task.Enabled, string(configJSON), task.NextRun,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (db *DB) UpdateTask(id int64, req *UpdateTaskRequest) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	task, err := db.getTaskByIDLocked(id)
	if err != nil {
		return err
	}

	if req.Name != nil {
		task.Name = *req.Name
	}
	if req.CronExpr != nil {
		task.CronExpr = *req.CronExpr
	}
	if req.Enabled != nil {
		task.Enabled = *req.Enabled
	}
	if req.Config != nil {
		task.Config = *req.Config
	}

	configJSON, err := json.Marshal(task.Config)
	if err != nil {
		return err
	}

	_, err = db.conn.Exec(`
		UPDATE scheduled_tasks
		SET name = ?, cron_expr = ?, enabled = ?, config = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		task.Name, task.CronExpr, task.Enabled, string(configJSON), id,
	)
	return err
}

func (db *DB) DeleteTask(id int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec("DELETE FROM scheduled_tasks WHERE id = ?", id)
	return err
}

func (db *DB) GetTask(id int64) (*ScheduledTask, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.getTaskByIDLocked(id)
}

func (db *DB) getTaskByIDLocked(id int64) (*ScheduledTask, error) {
	var task ScheduledTask
	var configJSON string
	var lastRun, nextRun sql.NullTime

	err := db.conn.QueryRow(`
		SELECT id, name, type, deployment_name, cron_expr, enabled, config, last_run, next_run, created_at, updated_at
		FROM scheduled_tasks WHERE id = ?`, id).Scan(
		&task.ID, &task.Name, &task.Type, &task.DeploymentName, &task.CronExpr,
		&task.Enabled, &configJSON, &lastRun, &nextRun, &task.CreatedAt, &task.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(configJSON), &task.Config); err != nil {
		return nil, err
	}

	if lastRun.Valid {
		task.LastRun = &lastRun.Time
	}
	if nextRun.Valid {
		task.NextRun = &nextRun.Time
	}

	return &task, nil
}

func (db *DB) GetTasksByDeployment(deploymentName string) ([]ScheduledTask, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	rows, err := db.conn.Query(`
		SELECT id, name, type, deployment_name, cron_expr, enabled, config, last_run, next_run, created_at, updated_at
		FROM scheduled_tasks
		WHERE deployment_name = ?
		ORDER BY created_at DESC`, deploymentName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return db.scanTasks(rows)
}

func (db *DB) GetAllTasks() ([]ScheduledTask, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	rows, err := db.conn.Query(`
		SELECT id, name, type, deployment_name, cron_expr, enabled, config, last_run, next_run, created_at, updated_at
		FROM scheduled_tasks
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return db.scanTasks(rows)
}

func (db *DB) GetEnabledTasks() ([]ScheduledTask, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	rows, err := db.conn.Query(`
		SELECT id, name, type, deployment_name, cron_expr, enabled, config, last_run, next_run, created_at, updated_at
		FROM scheduled_tasks
		WHERE enabled = TRUE
		ORDER BY next_run ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return db.scanTasks(rows)
}

func (db *DB) GetDueTasks() ([]ScheduledTask, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	rows, err := db.conn.Query(`
		SELECT id, name, type, deployment_name, cron_expr, enabled, config, last_run, next_run, created_at, updated_at
		FROM scheduled_tasks
		WHERE enabled = TRUE AND next_run <= datetime('now')
		ORDER BY next_run ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return db.scanTasks(rows)
}

func (db *DB) scanTasks(rows *sql.Rows) ([]ScheduledTask, error) {
	var tasks []ScheduledTask
	for rows.Next() {
		var task ScheduledTask
		var configJSON string
		var lastRun, nextRun sql.NullTime

		if err := rows.Scan(
			&task.ID, &task.Name, &task.Type, &task.DeploymentName, &task.CronExpr,
			&task.Enabled, &configJSON, &lastRun, &nextRun, &task.CreatedAt, &task.UpdatedAt,
		); err != nil {
			return nil, err
		}

		if err := json.Unmarshal([]byte(configJSON), &task.Config); err != nil {
			return nil, err
		}

		if lastRun.Valid {
			task.LastRun = &lastRun.Time
		}
		if nextRun.Valid {
			task.NextRun = &nextRun.Time
		}

		tasks = append(tasks, task)
	}
	return tasks, nil
}

func (db *DB) UpdateTaskRun(id int64, lastRun, nextRun time.Time) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`
		UPDATE scheduled_tasks
		SET last_run = ?, next_run = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		lastRun, nextRun, id,
	)
	return err
}

func (db *DB) UpdateTaskNextRun(id int64, nextRun time.Time) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`
		UPDATE scheduled_tasks
		SET next_run = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		nextRun, id,
	)
	return err
}

func (db *DB) CreateExecution(exec *TaskExecution) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	result, err := db.conn.Exec(`
		INSERT INTO task_executions (task_id, status, started_at)
		VALUES (?, ?, ?)`,
		exec.TaskID, exec.Status, exec.StartedAt,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (db *DB) UpdateExecution(id int64, status TaskStatus, output, errMsg string, endedAt time.Time, durationMs int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`
		UPDATE task_executions
		SET status = ?, output = ?, error = ?, ended_at = ?, duration_ms = ?
		WHERE id = ?`,
		status, output, errMsg, endedAt, durationMs, id,
	)
	return err
}

func (db *DB) GetExecutionsByTask(taskID int64, limit int) ([]TaskExecution, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

	rows, err := db.conn.Query(`
		SELECT id, task_id, status, output, error, started_at, ended_at, duration_ms
		FROM task_executions
		WHERE task_id = ?
		ORDER BY started_at DESC
		LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return db.scanExecutions(rows)
}

func (db *DB) GetRecentExecutions(limit int) ([]TaskExecution, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

	rows, err := db.conn.Query(`
		SELECT id, task_id, status, output, error, started_at, ended_at, duration_ms
		FROM task_executions
		ORDER BY started_at DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return db.scanExecutions(rows)
}

func (db *DB) scanExecutions(rows *sql.Rows) ([]TaskExecution, error) {
	var executions []TaskExecution
	for rows.Next() {
		var exec TaskExecution
		var output, errMsg sql.NullString
		var endedAt sql.NullTime
		var durationMs sql.NullInt64

		if err := rows.Scan(
			&exec.ID, &exec.TaskID, &exec.Status, &output, &errMsg,
			&exec.StartedAt, &endedAt, &durationMs,
		); err != nil {
			return nil, err
		}

		exec.Output = output.String
		exec.Error = errMsg.String
		if endedAt.Valid {
			exec.EndedAt = &endedAt.Time
		}
		if durationMs.Valid {
			exec.Duration = durationMs.Int64
		}

		executions = append(executions, exec)
	}
	return executions, nil
}

func (db *DB) CleanupOldExecutions(olderThan time.Duration) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	cutoff := time.Now().Add(-olderThan)
	result, err := db.conn.Exec("DELETE FROM task_executions WHERE started_at < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
