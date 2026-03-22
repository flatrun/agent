package scheduler

import (
	"context"
	"fmt"
	"log"
	"strings"

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

	service := config.Service
	if service == "" {
		serviceNames, err := e.dockerManager.GetComposeServiceNames(deploymentName)
		if err != nil {
			return "", fmt.Errorf("failed to resolve services: %w", err)
		}
		if len(serviceNames) == 1 {
			service = serviceNames[0]
		} else {
			found := false
			for _, sn := range serviceNames {
				if sn == "app" {
					service = "app"
					found = true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("multiple services found (%s), specify which service to use", strings.Join(serviceNames, ", "))
			}
		}
	}

	output, err := e.dockerManager.ComposeExec(ctx, deploymentName, service, config.Command)
	if err != nil {
		return output, fmt.Errorf("command failed: %w", err)
	}

	return output, nil
}
