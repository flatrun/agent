package scheduler

import (
	"context"
	"fmt"
	"log"

	"github.com/flatrun/agent/internal/backup"
	"github.com/flatrun/agent/internal/docker"
)

type Executor struct {
	backupManager *backup.Manager
	dockerManager *docker.Manager
	// agentRunner runs a scheduled agent headless. It is injected by the API
	// layer, which owns the AI runtime, so this package stays free of it.
	agentRunner func(ctx context.Context, agentName string) (string, error)
}

func NewExecutor(backupManager *backup.Manager, dockerManager *docker.Manager) *Executor {
	return &Executor{
		backupManager: backupManager,
		dockerManager: dockerManager,
	}
}

// SetAgentRunner wires the headless agent runner. Until it is set, agent tasks
// fail rather than run, so a scheduled agent never silently no-ops.
func (e *Executor) SetAgentRunner(fn func(ctx context.Context, agentName string) (string, error)) {
	e.agentRunner = fn
}

func (e *Executor) ExecuteAgent(ctx context.Context, config *AgentTaskConfig) (string, error) {
	if e.agentRunner == nil {
		return "", fmt.Errorf("agent runner not available")
	}
	return e.agentRunner(ctx, config.AgentName)
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

	service, err := e.dockerManager.ResolveService(deploymentName, config.Service)
	if err != nil {
		return "", fmt.Errorf("failed to resolve service: %w", err)
	}

	output, err := e.dockerManager.ComposeExec(ctx, deploymentName, service, config.Command)
	if err != nil {
		return output, fmt.Errorf("command failed: %w", err)
	}

	return output, nil
}
