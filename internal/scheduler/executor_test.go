package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flatrun/agent/internal/docker"
)

func setupExecutorTest(t *testing.T) (*Executor, string, func()) {
	tmpDir, err := os.MkdirTemp("", "executor-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	dockerManager := docker.NewManager(tmpDir)
	executor := NewExecutor(nil, dockerManager)

	cleanup := func() { os.RemoveAll(tmpDir) }
	return executor, tmpDir, cleanup
}

func createTestDeployment(t *testing.T, basePath, name, composeContent string) {
	deployDir := filepath.Join(basePath, name)
	if err := os.MkdirAll(deployDir, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deployDir, "docker-compose.yml"), []byte(composeContent), 0644); err != nil {
		t.Fatalf("Failed to write compose file: %v", err)
	}
}

func TestExecuteCommand_NilDockerManager(t *testing.T) {
	executor := NewExecutor(nil, nil)
	_, err := executor.ExecuteCommand(context.Background(), "test", &CommandTaskConfig{
		Command: "echo hello",
	})
	if err == nil {
		t.Fatal("Expected error when docker manager is nil")
	}
	if err.Error() != "docker manager not available" {
		t.Errorf("Unexpected error: %v", err)
	}
}

func TestExecuteCommand_SingleServiceAutoResolve(t *testing.T) {
	executor, tmpDir, cleanup := setupExecutorTest(t)
	defer cleanup()

	createTestDeployment(t, tmpDir, "single-svc", `name: single-svc
services:
  backend:
    image: myapp:latest
`)

	_, err := executor.ExecuteCommand(context.Background(), "single-svc", &CommandTaskConfig{
		Command: "echo hello",
	})
	// Will fail at ComposeExec (no docker API) but should NOT fail at service resolution
	if err != nil && strings.Contains(err.Error(), "multiple services found") {
		t.Errorf("Should not get multi-service error for single service: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "failed to resolve services") {
		t.Errorf("Should not get resolve error for existing deployment: %v", err)
	}
}

func TestExecuteCommand_MultipleServicesWithApp(t *testing.T) {
	executor, tmpDir, cleanup := setupExecutorTest(t)
	defer cleanup()

	createTestDeployment(t, tmpDir, "multi-app", `name: multi-app
services:
  app:
    image: myapp:latest
  db:
    image: postgres:15
  cache:
    image: redis:alpine
`)

	_, err := executor.ExecuteCommand(context.Background(), "multi-app", &CommandTaskConfig{
		Command: "echo hello",
	})
	// Should auto-resolve to "app" and only fail at ComposeExec level
	if err != nil && strings.Contains(err.Error(), "multiple services found") {
		t.Errorf("Should auto-resolve to 'app' when available: %v", err)
	}
}

func TestExecuteCommand_MultipleServicesWithoutApp(t *testing.T) {
	executor, tmpDir, cleanup := setupExecutorTest(t)
	defer cleanup()

	createTestDeployment(t, tmpDir, "multi-no-app", `name: multi-no-app
services:
  web:
    image: nginx:latest
  api:
    image: myapi:latest
  worker:
    image: myworker:latest
`)

	_, err := executor.ExecuteCommand(context.Background(), "multi-no-app", &CommandTaskConfig{
		Command: "echo hello",
	})
	if err == nil {
		t.Fatal("Expected error when multiple services and no 'app'")
	}
	if !strings.Contains(err.Error(), "multiple services found") {
		t.Errorf("Expected 'multiple services found' error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "web") || !strings.Contains(err.Error(), "api") {
		t.Errorf("Error should list available services, got: %v", err)
	}
}

func TestExecuteCommand_ExplicitService(t *testing.T) {
	executor, tmpDir, cleanup := setupExecutorTest(t)
	defer cleanup()

	createTestDeployment(t, tmpDir, "explicit-svc", `name: explicit-svc
services:
  web:
    image: nginx:latest
  api:
    image: myapi:latest
`)

	_, err := executor.ExecuteCommand(context.Background(), "explicit-svc", &CommandTaskConfig{
		Service: "api",
		Command: "echo hello",
	})
	// Should not fail at service resolution, only at ComposeExec (no docker API)
	if err != nil && strings.Contains(err.Error(), "multiple services found") {
		t.Errorf("Should not get multi-service error when service is explicit: %v", err)
	}
}

func TestExecuteCommand_DeploymentNotFound(t *testing.T) {
	executor, _, cleanup := setupExecutorTest(t)
	defer cleanup()

	_, err := executor.ExecuteCommand(context.Background(), "nonexistent", &CommandTaskConfig{
		Command: "echo hello",
	})
	if err == nil {
		t.Fatal("Expected error for nonexistent deployment")
	}
	if !strings.Contains(err.Error(), "failed to resolve services") {
		t.Errorf("Expected resolve error, got: %v", err)
	}
}

func TestExecuteBackup_NilBackupManager(t *testing.T) {
	executor := NewExecutor(nil, nil)
	_, err := executor.ExecuteBackup(context.Background(), "test", &BackupTaskConfig{})
	if err == nil {
		t.Fatal("Expected error when backup manager is nil")
	}
	if err.Error() != "backup manager not available" {
		t.Errorf("Unexpected error: %v", err)
	}
}
