package scheduler

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"time"

	"github.com/flatrun/agent/internal/backup"
	"github.com/flatrun/agent/internal/docker"
)

type Executor struct {
	backupManager *backup.Manager
	dockerManager *docker.Manager
}

func NewExecutor(backupManager *backup.Manager, dockerManager *docker.Manager) *Executor {
	return &Executor{
		backupManager: backupManager,
		dockerManager: dockerManager,
	}
}

func (e *Executor) ExecuteBackup(ctx context.Context, deploymentName string, config *BackupTaskConfig) (string, error) {
	if e.backupManager == nil {
		return "", fmt.Errorf("backup manager not available")
	}

	deployment, err := e.dockerManager.GetDeployment(deploymentName)
	if err != nil {
		return "", fmt.Errorf("deployment not found: %w", err)
	}

	var spec *backup.BackupSpec
	if deployment.Metadata != nil && deployment.Metadata.Backup != nil {
		spec = deployment.Metadata.Backup
	}

	b, err := e.backupManager.CreateBackup(ctx, deploymentName, spec)
	if err != nil {
		return "", err
	}

	if config.RetentionCount > 0 {
		deleted, err := e.backupManager.CleanupOldBackups(deploymentName, config.RetentionCount)
		if err != nil {
			log.Printf("Scheduler: failed to cleanup old backups: %v", err)
		} else if deleted > 0 {
			log.Printf("Scheduler: cleaned up %d old backups for %s", deleted, deploymentName)
		}
	}

	return fmt.Sprintf("Backup created: %s (%d bytes)", b.ID, b.Size), nil
}

func (e *Executor) ExecuteCommand(ctx context.Context, deploymentName string, config *CommandTaskConfig) (string, error) {
	if e.dockerManager == nil {
		return "", fmt.Errorf("docker manager not available")
	}

	containerName := deploymentName
	if config.Service != "" && config.Service != deploymentName {
		containerName = fmt.Sprintf("%s-%s", deploymentName, config.Service)
	}

	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 300
	}

	cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "docker", "exec", containerName, "sh", "-c", config.Command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("command failed: %w", err)
	}

	return string(output), nil
}
