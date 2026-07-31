package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

type TaskExecutor interface {
	ExecuteBackup(ctx context.Context, deploymentName string, config *BackupTaskConfig) (string, error)
	ExecuteCommand(ctx context.Context, deploymentName string, config *CommandTaskConfig) (string, error)
	ExecuteAgent(ctx context.Context, config *AgentTaskConfig) (string, error)
}

type Manager struct {
	db       *DB
	executor TaskExecutor
	parser   cron.Parser
	stopCh   chan struct{}
	wg       sync.WaitGroup
	mu       sync.RWMutex
	running  bool
}

func NewManager(deploymentsPath string, executor TaskExecutor) (*Manager, error) {
	db, err := NewDB(deploymentsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize scheduler database: %w", err)
	}

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

	return &Manager{
		db:       db,
		executor: executor,
		parser:   parser,
		stopCh:   make(chan struct{}),
	}, nil
}

func (m *Manager) Start() {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.stopCh = make(chan struct{})
	m.mu.Unlock()

	m.wg.Add(1)
	go m.runLoop()

	log.Println("Scheduler started")
}

func (m *Manager) Stop() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	m.running = false
	close(m.stopCh)
	m.mu.Unlock()

	m.wg.Wait()
	m.db.Close()

	log.Println("Scheduler stopped")
}

func (m *Manager) runLoop() {
	defer m.wg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	m.checkAndRunTasks()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.checkAndRunTasks()
		}
	}
}

func (m *Manager) checkAndRunTasks() {
	tasks, err := m.db.GetDueTasks()
	if err != nil {
		log.Printf("Scheduler: failed to get due tasks: %v", err)
		return
	}

	for _, task := range tasks {
		go m.executeTask(task)
	}
}

func (m *Manager) executeTask(task ScheduledTask) {
	exec := &TaskExecution{
		TaskID:    task.ID,
		Status:    TaskStatusRunning,
		StartedAt: time.Now(),
	}

	execID, err := m.db.CreateExecution(exec)
	if err != nil {
		log.Printf("Scheduler: failed to create execution record for task %d: %v", task.ID, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	var output string
	var execErr error

	switch task.Type {
	case TaskTypeBackup:
		if task.Config.BackupConfig != nil {
			output, execErr = m.executor.ExecuteBackup(ctx, task.DeploymentName, task.Config.BackupConfig)
		} else {
			execErr = fmt.Errorf("backup config is nil")
		}
	case TaskTypeCommand:
		if task.Config.CommandConfig != nil {
			output, execErr = m.executor.ExecuteCommand(ctx, task.DeploymentName, task.Config.CommandConfig)
		} else {
			execErr = fmt.Errorf("command config is nil")
		}
	case TaskTypeAgent:
		if task.Config.AgentConfig != nil {
			output, execErr = m.executor.ExecuteAgent(ctx, task.Config.AgentConfig)
		} else {
			execErr = fmt.Errorf("agent config is nil")
		}
	default:
		execErr = fmt.Errorf("unknown task type: %s", task.Type)
	}

	endedAt := time.Now()
	durationMs := endedAt.Sub(exec.StartedAt).Milliseconds()
	status := TaskStatusCompleted
	var errMsg string

	if execErr != nil {
		status = TaskStatusFailed
		errMsg = execErr.Error()
		log.Printf("Scheduler: task %d (%s) failed: %v", task.ID, task.Name, execErr)
	} else {
		log.Printf("Scheduler: task %d (%s) completed successfully", task.ID, task.Name)
	}

	if err := m.db.UpdateExecution(execID, status, output, errMsg, endedAt, durationMs); err != nil {
		log.Printf("Scheduler: failed to update execution %d: %v", execID, err)
	}

	nextRun, err := m.calculateNextRun(task.CronExpr)
	if err != nil {
		log.Printf("Scheduler: failed to calculate next run for task %d: %v", task.ID, err)
		return
	}

	if err := m.db.UpdateTaskRun(task.ID, endedAt, nextRun); err != nil {
		log.Printf("Scheduler: failed to update task %d run times: %v", task.ID, err)
	}
}

func (m *Manager) calculateNextRun(cronExpr string) (time.Time, error) {
	schedule, err := m.parser.Parse(cronExpr)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron expression: %w", err)
	}
	return schedule.Next(time.Now()), nil
}

func (m *Manager) ValidateCronExpr(cronExpr string) error {
	_, err := m.parser.Parse(cronExpr)
	return err
}

func (m *Manager) CreateTask(req *CreateTaskRequest) (*ScheduledTask, error) {
	nextRun, err := m.calculateNextRun(req.CronExpr)
	if err != nil {
		return nil, err
	}

	task := &ScheduledTask{
		Name:           req.Name,
		Type:           req.Type,
		DeploymentName: req.DeploymentName,
		CronExpr:       req.CronExpr,
		Enabled:        req.Enabled,
		Config:         req.Config,
		NextRun:        &nextRun,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	id, err := m.db.CreateTask(task)
	if err != nil {
		return nil, err
	}

	task.ID = id
	return task, nil
}

func (m *Manager) UpdateTask(id int64, req *UpdateTaskRequest) (*ScheduledTask, error) {
	if req.CronExpr != nil {
		if err := m.ValidateCronExpr(*req.CronExpr); err != nil {
			return nil, err
		}
	}

	if err := m.db.UpdateTask(id, req); err != nil {
		return nil, err
	}

	task, err := m.db.GetTask(id)
	if err != nil {
		return nil, err
	}

	if req.CronExpr != nil || (req.Enabled != nil && *req.Enabled) {
		nextRun, err := m.calculateNextRun(task.CronExpr)
		if err == nil {
			if err := m.db.UpdateTaskNextRun(id, nextRun); err != nil {
				log.Printf("Scheduler: failed to update next run time: %v", err)
			}
			task.NextRun = &nextRun
		}
	}

	return task, nil
}

func (m *Manager) DeleteTask(id int64) error {
	return m.db.DeleteTask(id)
}

// findAgentTask returns the scheduled task backing an agent's schedule, or nil.
// An agent maps to at most one task, keyed by its name in the agent config.
func (m *Manager) findAgentTask(agentName string) (*ScheduledTask, error) {
	tasks, err := m.db.GetAllTasks()
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		t := tasks[i]
		if t.Type == TaskTypeAgent && t.Config.AgentConfig != nil && t.Config.AgentConfig.AgentName == agentName {
			return &t, nil
		}
	}
	return nil, nil
}

// SyncAgentTask makes the scheduler reflect an agent's schedule: it creates the
// backing task, updates its cron when it changed, or does nothing when already
// in sync. Called whenever an agent with a schedule is written.
func (m *Manager) SyncAgentTask(agentName, cronExpr, deployment string) error {
	if err := m.ValidateCronExpr(cronExpr); err != nil {
		return err
	}
	existing, err := m.findAgentTask(agentName)
	if err != nil {
		return err
	}
	enabled := true
	if existing != nil {
		if existing.CronExpr == cronExpr && existing.Enabled {
			return nil
		}
		_, err := m.UpdateTask(existing.ID, &UpdateTaskRequest{CronExpr: &cronExpr, Enabled: &enabled})
		return err
	}
	_, err = m.CreateTask(&CreateTaskRequest{
		Name:           "agent:" + agentName,
		Type:           TaskTypeAgent,
		DeploymentName: deployment,
		CronExpr:       cronExpr,
		Enabled:        enabled,
		Config:         TaskConfig{AgentConfig: &AgentTaskConfig{AgentName: agentName}},
	})
	return err
}

// RemoveAgentTask drops an agent's scheduled task, if any. Called when an agent
// loses its schedule or is deleted.
func (m *Manager) RemoveAgentTask(agentName string) error {
	existing, err := m.findAgentTask(agentName)
	if err != nil || existing == nil {
		return err
	}
	return m.db.DeleteTask(existing.ID)
}

func (m *Manager) GetTask(id int64) (*ScheduledTask, error) {
	return m.db.GetTask(id)
}

func (m *Manager) GetAllTasks() ([]ScheduledTask, error) {
	return m.db.GetAllTasks()
}

func (m *Manager) GetTasksByDeployment(deploymentName string) ([]ScheduledTask, error) {
	return m.db.GetTasksByDeployment(deploymentName)
}

func (m *Manager) GetTaskExecutions(taskID int64, limit int) ([]TaskExecution, error) {
	return m.db.GetExecutionsByTask(taskID, limit)
}

func (m *Manager) GetRecentExecutions(limit int) ([]TaskExecution, error) {
	return m.db.GetRecentExecutions(limit)
}

func (m *Manager) RunTaskNow(id int64) error {
	task, err := m.db.GetTask(id)
	if err != nil {
		return err
	}

	go m.executeTask(*task)
	return nil
}

func (m *Manager) Cleanup(olderThan time.Duration) (int64, error) {
	return m.db.CleanupOldExecutions(olderThan)
}
