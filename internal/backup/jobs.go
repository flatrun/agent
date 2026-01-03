package backup

import (
	"context"
	"sync"
	"time"
)

type JobType string
type JobStatus string

const (
	JobTypeBackup  JobType = "backup"
	JobTypeRestore JobType = "restore"

	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
)

type Job struct {
	ID             string     `json:"id"`
	Type           JobType    `json:"type"`
	Status         JobStatus  `json:"status"`
	DeploymentName string     `json:"deployment_name"`
	BackupID       string     `json:"backup_id,omitempty"`
	Progress       string     `json:"progress,omitempty"`
	Error          string     `json:"error,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}

type JobTracker struct {
	jobs map[string]*Job
	mu   sync.RWMutex
}

func NewJobTracker() *JobTracker {
	return &JobTracker{
		jobs: make(map[string]*Job),
	}
}

func (t *JobTracker) CreateJob(id string, jobType JobType, deploymentName string) *Job {
	t.mu.Lock()
	defer t.mu.Unlock()

	job := &Job{
		ID:             id,
		Type:           jobType,
		Status:         JobStatusPending,
		DeploymentName: deploymentName,
		StartedAt:      time.Now(),
	}
	t.jobs[id] = job
	return job
}

func (t *JobTracker) GetJob(id string) *Job {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.jobs[id]
}

func (t *JobTracker) UpdateStatus(id string, status JobStatus, progress string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if job, ok := t.jobs[id]; ok {
		job.Status = status
		job.Progress = progress
		if status == JobStatusCompleted || status == JobStatusFailed {
			now := time.Now()
			job.CompletedAt = &now
		}
	}
}

func (t *JobTracker) SetError(id string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if job, ok := t.jobs[id]; ok {
		job.Status = JobStatusFailed
		job.Error = err.Error()
		now := time.Now()
		job.CompletedAt = &now
	}
}

func (t *JobTracker) SetBackupID(id string, backupID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if job, ok := t.jobs[id]; ok {
		job.BackupID = backupID
	}
}

func (t *JobTracker) ListJobs(deploymentName string, limit int) []*Job {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var jobs []*Job
	for _, job := range t.jobs {
		if deploymentName == "" || job.DeploymentName == deploymentName {
			jobs = append(jobs, job)
		}
	}

	if limit > 0 && len(jobs) > limit {
		jobs = jobs[:limit]
	}

	return jobs
}

func (t *JobTracker) Cleanup(olderThan time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	cutoff := time.Now().Add(-olderThan)
	for id, job := range t.jobs {
		if job.CompletedAt != nil && job.CompletedAt.Before(cutoff) {
			delete(t.jobs, id)
		}
	}
}

func (m *Manager) StartBackupJob(deploymentName string, spec *BackupSpec) string {
	jobID := generateJobID("backup", deploymentName)
	m.jobs.CreateJob(jobID, JobTypeBackup, deploymentName)

	go func() {
		m.jobs.UpdateStatus(jobID, JobStatusRunning, "Starting backup")

		backup, err := m.CreateBackup(context.Background(), deploymentName, spec)
		if err != nil {
			m.jobs.SetError(jobID, err)
			return
		}

		m.jobs.SetBackupID(jobID, backup.ID)
		m.jobs.UpdateStatus(jobID, JobStatusCompleted, "Backup completed")
	}()

	return jobID
}

func (m *Manager) StartRestoreJob(req *RestoreBackupRequest) string {
	backup, err := m.GetBackup(req.BackupID)
	if err != nil {
		jobID := generateJobID("restore", req.BackupID)
		m.jobs.CreateJob(jobID, JobTypeRestore, "")
		m.jobs.SetError(jobID, err)
		return jobID
	}

	deploymentName := backup.DeploymentName
	if req.DeploymentName != "" {
		deploymentName = req.DeploymentName
	}

	jobID := generateJobID("restore", deploymentName)
	job := m.jobs.CreateJob(jobID, JobTypeRestore, deploymentName)
	job.BackupID = req.BackupID

	go func() {
		m.jobs.UpdateStatus(jobID, JobStatusRunning, "Starting restore")

		if err := m.RestoreBackup(context.Background(), req); err != nil {
			m.jobs.SetError(jobID, err)
			return
		}

		m.jobs.UpdateStatus(jobID, JobStatusCompleted, "Restore completed")
	}()

	return jobID
}

func (m *Manager) GetJob(jobID string) *Job {
	return m.jobs.GetJob(jobID)
}

func (m *Manager) ListJobs(deploymentName string, limit int) []*Job {
	return m.jobs.ListJobs(deploymentName, limit)
}

func generateJobID(prefix, name string) string {
	return prefix + "_" + name + "_" + time.Now().Format("20060102_150405")
}
