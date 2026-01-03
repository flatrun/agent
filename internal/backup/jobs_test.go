package backup

import (
	"testing"
	"time"
)

func TestNewJobTracker(t *testing.T) {
	tracker := NewJobTracker()
	if tracker == nil {
		t.Fatal("Expected tracker to be non-nil")
	}
	if tracker.jobs == nil {
		t.Fatal("Expected jobs map to be initialized")
	}
}

func TestCreateJob(t *testing.T) {
	tracker := NewJobTracker()

	job := tracker.CreateJob("test-job-1", JobTypeBackup, "my-deployment")

	if job == nil {
		t.Fatal("Expected job to be non-nil")
	}
	if job.ID != "test-job-1" {
		t.Errorf("Expected ID 'test-job-1', got: %s", job.ID)
	}
	if job.Type != JobTypeBackup {
		t.Errorf("Expected type 'backup', got: %s", job.Type)
	}
	if job.DeploymentName != "my-deployment" {
		t.Errorf("Expected deployment 'my-deployment', got: %s", job.DeploymentName)
	}
	if job.Status != JobStatusPending {
		t.Errorf("Expected status 'pending', got: %s", job.Status)
	}
	if job.StartedAt.IsZero() {
		t.Error("Expected StartedAt to be set")
	}
}

func TestGetJob(t *testing.T) {
	tracker := NewJobTracker()

	tracker.CreateJob("test-job-1", JobTypeBackup, "my-deployment")

	job := tracker.GetJob("test-job-1")
	if job == nil {
		t.Fatal("Expected to find job")
	}
	if job.ID != "test-job-1" {
		t.Errorf("Expected ID 'test-job-1', got: %s", job.ID)
	}

	notFound := tracker.GetJob("nonexistent")
	if notFound != nil {
		t.Error("Expected nil for nonexistent job")
	}
}

func TestUpdateStatus(t *testing.T) {
	tracker := NewJobTracker()

	tracker.CreateJob("test-job-1", JobTypeBackup, "my-deployment")

	tracker.UpdateStatus("test-job-1", JobStatusRunning, "Processing...")

	job := tracker.GetJob("test-job-1")
	if job.Status != JobStatusRunning {
		t.Errorf("Expected status 'running', got: %s", job.Status)
	}
	if job.Progress != "Processing..." {
		t.Errorf("Expected progress 'Processing...', got: %s", job.Progress)
	}
	if job.CompletedAt != nil {
		t.Error("Expected CompletedAt to be nil for running job")
	}

	tracker.UpdateStatus("test-job-1", JobStatusCompleted, "Done")

	job = tracker.GetJob("test-job-1")
	if job.Status != JobStatusCompleted {
		t.Errorf("Expected status 'completed', got: %s", job.Status)
	}
	if job.CompletedAt == nil {
		t.Error("Expected CompletedAt to be set for completed job")
	}
}

func TestSetError(t *testing.T) {
	tracker := NewJobTracker()

	tracker.CreateJob("test-job-1", JobTypeRestore, "my-deployment")
	tracker.UpdateStatus("test-job-1", JobStatusRunning, "Restoring...")

	testErr := &testError{msg: "restore failed: disk full"}
	tracker.SetError("test-job-1", testErr)

	job := tracker.GetJob("test-job-1")
	if job.Status != JobStatusFailed {
		t.Errorf("Expected status 'failed', got: %s", job.Status)
	}
	if job.Error != "restore failed: disk full" {
		t.Errorf("Expected error message, got: %s", job.Error)
	}
	if job.CompletedAt == nil {
		t.Error("Expected CompletedAt to be set for failed job")
	}
}

func TestSetBackupID(t *testing.T) {
	tracker := NewJobTracker()

	tracker.CreateJob("test-job-1", JobTypeBackup, "my-deployment")
	tracker.SetBackupID("test-job-1", "backup-20240101-120000")

	job := tracker.GetJob("test-job-1")
	if job.BackupID != "backup-20240101-120000" {
		t.Errorf("Expected backup ID, got: %s", job.BackupID)
	}
}

func TestListJobs(t *testing.T) {
	tracker := NewJobTracker()

	tracker.CreateJob("job-1", JobTypeBackup, "deployment-a")
	tracker.CreateJob("job-2", JobTypeRestore, "deployment-a")
	tracker.CreateJob("job-3", JobTypeBackup, "deployment-b")

	allJobs := tracker.ListJobs("", 0)
	if len(allJobs) != 3 {
		t.Errorf("Expected 3 jobs, got: %d", len(allJobs))
	}

	deploymentAJobs := tracker.ListJobs("deployment-a", 0)
	if len(deploymentAJobs) != 2 {
		t.Errorf("Expected 2 jobs for deployment-a, got: %d", len(deploymentAJobs))
	}

	limitedJobs := tracker.ListJobs("", 2)
	if len(limitedJobs) != 2 {
		t.Errorf("Expected 2 jobs with limit, got: %d", len(limitedJobs))
	}
}

func TestCleanup(t *testing.T) {
	tracker := NewJobTracker()

	job1 := tracker.CreateJob("old-job", JobTypeBackup, "deployment")
	oldTime := time.Now().Add(-2 * time.Hour)
	job1.CompletedAt = &oldTime
	job1.Status = JobStatusCompleted

	job2 := tracker.CreateJob("new-job", JobTypeBackup, "deployment")
	newTime := time.Now().Add(-10 * time.Minute)
	job2.CompletedAt = &newTime
	job2.Status = JobStatusCompleted

	tracker.CreateJob("running-job", JobTypeBackup, "deployment")

	tracker.Cleanup(1 * time.Hour)

	if tracker.GetJob("old-job") != nil {
		t.Error("Expected old completed job to be cleaned up")
	}
	if tracker.GetJob("new-job") == nil {
		t.Error("Expected recent completed job to remain")
	}
	if tracker.GetJob("running-job") == nil {
		t.Error("Expected running job to remain")
	}
}

func TestJobStatusConstants(t *testing.T) {
	if JobStatusPending != "pending" {
		t.Errorf("Expected 'pending', got: %s", JobStatusPending)
	}
	if JobStatusRunning != "running" {
		t.Errorf("Expected 'running', got: %s", JobStatusRunning)
	}
	if JobStatusCompleted != "completed" {
		t.Errorf("Expected 'completed', got: %s", JobStatusCompleted)
	}
	if JobStatusFailed != "failed" {
		t.Errorf("Expected 'failed', got: %s", JobStatusFailed)
	}
}

func TestJobTypeConstants(t *testing.T) {
	if JobTypeBackup != "backup" {
		t.Errorf("Expected 'backup', got: %s", JobTypeBackup)
	}
	if JobTypeRestore != "restore" {
		t.Errorf("Expected 'restore', got: %s", JobTypeRestore)
	}
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}
