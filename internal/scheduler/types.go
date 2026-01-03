package scheduler

import (
	"time"
)

type TaskType string

const (
	TaskTypeBackup  TaskType = "backup"
	TaskTypeCommand TaskType = "command"
)

type TaskStatus string

const (
	TaskStatusPending   TaskStatus = "pending"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
)

type ScheduledTask struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	Type           TaskType   `json:"type"`
	DeploymentName string     `json:"deployment_name"`
	CronExpr       string     `json:"cron_expr"`
	Enabled        bool       `json:"enabled"`
	Config         TaskConfig `json:"config"`
	LastRun        *time.Time `json:"last_run,omitempty"`
	NextRun        *time.Time `json:"next_run,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type TaskConfig struct {
	// For backup tasks
	BackupConfig *BackupTaskConfig `json:"backup_config,omitempty"`
	// For command tasks
	CommandConfig *CommandTaskConfig `json:"command_config,omitempty"`
}

type BackupTaskConfig struct {
	RetentionCount int    `json:"retention_count"`
	StoragePath    string `json:"storage_path,omitempty"`
}

type CommandTaskConfig struct {
	Service string `json:"service"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

type TaskExecution struct {
	ID        int64      `json:"id"`
	TaskID    int64      `json:"task_id"`
	Status    TaskStatus `json:"status"`
	Output    string     `json:"output,omitempty"`
	Error     string     `json:"error,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Duration  int64      `json:"duration_ms,omitempty"`
}

type CreateTaskRequest struct {
	Name           string     `json:"name" binding:"required"`
	Type           TaskType   `json:"type" binding:"required"`
	DeploymentName string     `json:"deployment_name" binding:"required"`
	CronExpr       string     `json:"cron_expr" binding:"required"`
	Enabled        bool       `json:"enabled"`
	Config         TaskConfig `json:"config"`
}

type UpdateTaskRequest struct {
	Name     *string     `json:"name,omitempty"`
	CronExpr *string     `json:"cron_expr,omitempty"`
	Enabled  *bool       `json:"enabled,omitempty"`
	Config   *TaskConfig `json:"config,omitempty"`
}
