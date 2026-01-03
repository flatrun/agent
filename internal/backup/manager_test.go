package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func setupTestManager(t *testing.T) (*Manager, string) {
	tmpDir, err := os.MkdirTemp("", "backup-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	m, err := NewManager(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create manager: %v", err)
	}

	return m, tmpDir
}

func TestNewManager(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	if m == nil {
		t.Fatal("Expected manager to be non-nil")
	}
	if m.deploymentsPath != tmpDir {
		t.Errorf("Expected deploymentsPath %s, got: %s", tmpDir, m.deploymentsPath)
	}

	expectedBackupsPath := filepath.Join(tmpDir, ".flatrun", "backups")
	if m.backupsPath != expectedBackupsPath {
		t.Errorf("Expected backupsPath %s, got: %s", expectedBackupsPath, m.backupsPath)
	}

	if _, err := os.Stat(m.backupsPath); os.IsNotExist(err) {
		t.Error("Expected backups directory to be created")
	}

	if m.jobs == nil {
		t.Error("Expected job tracker to be initialized")
	}
}

func TestCreateBackup_DeploymentNotFound(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	_, err := m.CreateBackup(ctx, "nonexistent", nil)
	if err == nil {
		t.Error("Expected error for nonexistent deployment")
	}
}

func TestCreateBackup_Success(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	deploymentPath := filepath.Join(tmpDir, "test-deployment")
	if err := os.MkdirAll(deploymentPath, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}

	composeContent := `version: '3'
services:
  web:
    image: nginx
`
	if err := os.WriteFile(filepath.Join(deploymentPath, "docker-compose.yml"), []byte(composeContent), 0644); err != nil {
		t.Fatalf("Failed to create compose file: %v", err)
	}

	envContent := "FOO=bar\n"
	if err := os.WriteFile(filepath.Join(deploymentPath, ".env"), []byte(envContent), 0644); err != nil {
		t.Fatalf("Failed to create env file: %v", err)
	}

	ctx := context.Background()
	backup, err := m.CreateBackup(ctx, "test-deployment", nil)
	if err != nil {
		t.Fatalf("Failed to create backup: %v", err)
	}

	if backup.ID == "" {
		t.Error("Expected backup ID to be set")
	}
	if backup.DeploymentName != "test-deployment" {
		t.Errorf("Expected deployment name 'test-deployment', got: %s", backup.DeploymentName)
	}
	if backup.Status != BackupStatusCompleted {
		t.Errorf("Expected status 'completed', got: %s", backup.Status)
	}
	if backup.Size <= 0 {
		t.Error("Expected backup size to be greater than 0")
	}
	if backup.Path == "" {
		t.Error("Expected backup path to be set")
	}

	if _, err := os.Stat(backup.Path); os.IsNotExist(err) {
		t.Error("Expected backup file to exist")
	}

	hasCompose := false
	hasEnv := false
	for _, comp := range backup.Components {
		if comp == "compose" {
			hasCompose = true
		}
		if comp == "env" {
			hasEnv = true
		}
	}
	if !hasCompose {
		t.Error("Expected 'compose' component")
	}
	if !hasEnv {
		t.Error("Expected 'env' component")
	}
}

func TestListBackups_Empty(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	backups, err := m.ListBackups(&BackupListFilter{})
	if err != nil {
		t.Fatalf("Failed to list backups: %v", err)
	}
	if len(backups) != 0 {
		t.Errorf("Expected 0 backups, got: %d", len(backups))
	}
}

func TestListBackups_WithBackups(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	deploymentPath := filepath.Join(tmpDir, "test-deployment")
	if err := os.MkdirAll(deploymentPath, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deploymentPath, "docker-compose.yml"), []byte("version: '3'\n"), 0644); err != nil {
		t.Fatalf("Failed to create compose file: %v", err)
	}

	ctx := context.Background()
	_, err := m.CreateBackup(ctx, "test-deployment", nil)
	if err != nil {
		t.Fatalf("Failed to create backup: %v", err)
	}

	backups, err := m.ListBackups(&BackupListFilter{})
	if err != nil {
		t.Fatalf("Failed to list backups: %v", err)
	}
	if len(backups) != 1 {
		t.Errorf("Expected 1 backup, got: %d", len(backups))
	}
}

func TestListBackups_FilterByDeployment(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	for _, name := range []string{"deployment-a", "deployment-b"} {
		deploymentPath := filepath.Join(tmpDir, name)
		if err := os.MkdirAll(deploymentPath, 0755); err != nil {
			t.Fatalf("Failed to create deployment dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(deploymentPath, "docker-compose.yml"), []byte("version: '3'\n"), 0644); err != nil {
			t.Fatalf("Failed to create compose file: %v", err)
		}
		_, err := m.CreateBackup(context.Background(), name, nil)
		if err != nil {
			t.Fatalf("Failed to create backup: %v", err)
		}
	}

	backups, err := m.ListBackups(&BackupListFilter{DeploymentName: "deployment-a"})
	if err != nil {
		t.Fatalf("Failed to list backups: %v", err)
	}
	if len(backups) != 1 {
		t.Errorf("Expected 1 backup for deployment-a, got: %d", len(backups))
	}
	if backups[0].DeploymentName != "deployment-a" {
		t.Errorf("Expected deployment-a, got: %s", backups[0].DeploymentName)
	}
}

func TestGetBackup(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	deploymentPath := filepath.Join(tmpDir, "test-deployment")
	if err := os.MkdirAll(deploymentPath, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deploymentPath, "docker-compose.yml"), []byte("version: '3'\n"), 0644); err != nil {
		t.Fatalf("Failed to create compose file: %v", err)
	}

	ctx := context.Background()
	created, err := m.CreateBackup(ctx, "test-deployment", nil)
	if err != nil {
		t.Fatalf("Failed to create backup: %v", err)
	}

	backup, err := m.GetBackup(created.ID)
	if err != nil {
		t.Fatalf("Failed to get backup: %v", err)
	}
	if backup.ID != created.ID {
		t.Errorf("Expected ID %s, got: %s", created.ID, backup.ID)
	}

	_, err = m.GetBackup("nonexistent_backup")
	if err == nil {
		t.Error("Expected error for nonexistent backup")
	}
}

func TestDeleteBackup(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	deploymentPath := filepath.Join(tmpDir, "test-deployment")
	if err := os.MkdirAll(deploymentPath, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deploymentPath, "docker-compose.yml"), []byte("version: '3'\n"), 0644); err != nil {
		t.Fatalf("Failed to create compose file: %v", err)
	}

	ctx := context.Background()
	created, err := m.CreateBackup(ctx, "test-deployment", nil)
	if err != nil {
		t.Fatalf("Failed to create backup: %v", err)
	}

	err = m.DeleteBackup(created.ID)
	if err != nil {
		t.Fatalf("Failed to delete backup: %v", err)
	}

	if _, err := os.Stat(created.Path); !os.IsNotExist(err) {
		t.Error("Expected backup file to be deleted")
	}

	_, err = m.GetBackup(created.ID)
	if err == nil {
		t.Error("Expected error when getting deleted backup")
	}
}

func TestGetBackupPath(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	deploymentPath := filepath.Join(tmpDir, "test-deployment")
	if err := os.MkdirAll(deploymentPath, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deploymentPath, "docker-compose.yml"), []byte("version: '3'\n"), 0644); err != nil {
		t.Fatalf("Failed to create compose file: %v", err)
	}

	ctx := context.Background()
	created, err := m.CreateBackup(ctx, "test-deployment", nil)
	if err != nil {
		t.Fatalf("Failed to create backup: %v", err)
	}

	path, err := m.GetBackupPath(created.ID)
	if err != nil {
		t.Fatalf("Failed to get backup path: %v", err)
	}
	if path != created.Path {
		t.Errorf("Expected path %s, got: %s", created.Path, path)
	}
}

func TestCleanupOldBackups(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	deploymentPath := filepath.Join(tmpDir, "test-deployment")
	if err := os.MkdirAll(deploymentPath, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deploymentPath, "docker-compose.yml"), []byte("version: '3'\n"), 0644); err != nil {
		t.Fatalf("Failed to create compose file: %v", err)
	}

	backupDir := filepath.Join(m.backupsPath, "test-deployment")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		t.Fatalf("Failed to create backup dir: %v", err)
	}
	for i := 0; i < 5; i++ {
		backupFile := filepath.Join(backupDir, "test-deployment_2024010"+string(rune('1'+i))+"_120000.tar.gz")
		if err := os.WriteFile(backupFile, []byte("test backup content"), 0644); err != nil {
			t.Fatalf("Failed to create backup file: %v", err)
		}
	}

	backups, _ := m.ListBackups(&BackupListFilter{DeploymentName: "test-deployment"})
	if len(backups) != 5 {
		t.Fatalf("Expected 5 backups, got: %d", len(backups))
	}

	deleted, err := m.CleanupOldBackups("test-deployment", 2)
	if err != nil {
		t.Fatalf("Failed to cleanup backups: %v", err)
	}
	if deleted != 3 {
		t.Errorf("Expected 3 deleted, got: %d", deleted)
	}

	backups, _ = m.ListBackups(&BackupListFilter{DeploymentName: "test-deployment"})
	if len(backups) != 2 {
		t.Errorf("Expected 2 backups remaining, got: %d", len(backups))
	}
}

func TestStartBackupJob(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	deploymentPath := filepath.Join(tmpDir, "test-deployment")
	if err := os.MkdirAll(deploymentPath, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deploymentPath, "docker-compose.yml"), []byte("version: '3'\n"), 0644); err != nil {
		t.Fatalf("Failed to create compose file: %v", err)
	}

	jobID := m.StartBackupJob("test-deployment", nil)

	if jobID == "" {
		t.Error("Expected job ID to be returned")
	}

	job := m.GetJob(jobID)
	if job == nil {
		t.Fatal("Expected to find job")
	}
	if job.Type != JobTypeBackup {
		t.Errorf("Expected job type 'backup', got: %s", job.Type)
	}
}

func TestManagerGetJob(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	m.jobs.CreateJob("test-job", JobTypeBackup, "deployment")

	job := m.GetJob("test-job")
	if job == nil {
		t.Fatal("Expected to find job")
	}
	if job.ID != "test-job" {
		t.Errorf("Expected ID 'test-job', got: %s", job.ID)
	}
}

func TestManagerListJobs(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	m.jobs.CreateJob("job-1", JobTypeBackup, "deployment-a")
	m.jobs.CreateJob("job-2", JobTypeRestore, "deployment-b")

	jobs := m.ListJobs("", 0)
	if len(jobs) != 2 {
		t.Errorf("Expected 2 jobs, got: %d", len(jobs))
	}

	jobs = m.ListJobs("deployment-a", 0)
	if len(jobs) != 1 {
		t.Errorf("Expected 1 job for deployment-a, got: %d", len(jobs))
	}
}
