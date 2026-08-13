package backup

import (
	"time"

	"github.com/flatrun/agent/pkg/models"
)

type BackupStatus string

const (
	BackupStatusPending    BackupStatus = "pending"
	BackupStatusInProgress BackupStatus = "in_progress"
	BackupStatusCompleted  BackupStatus = "completed"
	BackupStatusFailed     BackupStatus = "failed"
)

type BackupSpec = models.BackupSpec
type ContainerPath = models.ContainerBackupPath
type DatabaseSpec = models.DatabaseBackupSpec
type HookSpec = models.BackupHookSpec

type Backup struct {
	ID             string       `json:"id"`
	DeploymentName string       `json:"deployment_name"`
	Status         BackupStatus `json:"status"`
	Size           int64        `json:"size"`
	Path           string       `json:"path" cli:"-"`
	Components     []string     `json:"components"`
	Error          string       `json:"error,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
	CompletedAt    *time.Time   `json:"completed_at,omitempty"`
	ExpiresAt      *time.Time   `json:"expires_at,omitempty"`
	// Locations lists where this backup exists: "local" and/or remote
	// destination names. A backup may live remotely only if local retention
	// has pruned the on-disk copy.
	Locations []string `json:"locations,omitempty"`
}

type BackupMetadata struct {
	ID              string            `json:"id"`
	DeploymentName  string            `json:"deployment_name"`
	DeploymentPath  string            `json:"deployment_path"`
	CreatedAt       time.Time         `json:"created_at"`
	AgentVersion    string            `json:"agent_version"`
	Components      BackupComponents  `json:"components"`
	ContainerStates map[string]string `json:"container_states,omitempty"`
}

type BackupComponents struct {
	ComposeFile   bool     `json:"compose_file"`
	EnvFile       bool     `json:"env_file"`
	Metadata      bool     `json:"metadata"`
	MountedData   []string `json:"mounted_data,omitempty"`
	ContainerData []string `json:"container_data,omitempty"`
	Databases     []string `json:"databases,omitempty"`
}

type CreateBackupRequest struct {
	DeploymentName string `json:"deployment_name" binding:"required"`
	Description    string `json:"description,omitempty"`
}

type RestoreBackupRequest struct {
	BackupID       string `json:"backup_id" binding:"required"`
	DeploymentName string `json:"deployment_name,omitempty"`
	RestoreData    bool   `json:"restore_data"`
	RestoreDB      bool   `json:"restore_db"`
	StopFirst      bool   `json:"stop_first"`
}

type BackupListFilter struct {
	DeploymentName string
	Status         BackupStatus
	Limit          int
	Offset         int
}
