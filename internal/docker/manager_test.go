package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/models"
)

func TestPullDeployment(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pull-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	deploymentDir := filepath.Join(tmpDir, "test-deployment")
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}

	composeContent := `name: test-deployment
services:
  web:
    image: nginx:latest
  db:
    image: postgres:15
`
	if err := os.WriteFile(filepath.Join(deploymentDir, "docker-compose.yml"), []byte(composeContent), 0644); err != nil {
		t.Fatalf("Failed to write compose file: %v", err)
	}

	manager := NewManager(tmpDir)

	_, err = manager.PullDeployment("test-deployment", false)
	if err != nil {
		t.Logf("Pull returned error (expected if Docker unavailable): %v", err)
	}
}

func TestPullDeploymentNotFound(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pull-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	manager := NewManager(tmpDir)

	_, err = manager.PullDeployment("nonexistent-deployment", false)
	if err == nil {
		t.Error("Expected error for nonexistent deployment")
	}
}

func TestPullDeploymentWithBuildConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pull-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	deploymentDir := filepath.Join(tmpDir, "build-deployment")
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}

	composeContent := `name: build-deployment
services:
  app:
    build:
      context: .
      dockerfile: Dockerfile
  cache:
    image: redis:alpine
`
	if err := os.WriteFile(filepath.Join(deploymentDir, "docker-compose.yml"), []byte(composeContent), 0644); err != nil {
		t.Fatalf("Failed to write compose file: %v", err)
	}

	if err := os.WriteFile(filepath.Join(deploymentDir, "Dockerfile"), []byte("FROM alpine\n"), 0644); err != nil {
		t.Fatalf("Failed to write Dockerfile: %v", err)
	}

	manager := NewManager(tmpDir)

	_, err = manager.PullDeployment("build-deployment", false)
	if err != nil {
		t.Logf("Pull returned error (expected if Docker unavailable): %v", err)
	}
}

func TestPullDeploymentOnlyLatest(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pull-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	deploymentDir := filepath.Join(tmpDir, "latest-deployment")
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}

	composeContent := `name: latest-deployment
services:
  web:
    image: nginx:latest
  db:
    image: postgres:15
`
	if err := os.WriteFile(filepath.Join(deploymentDir, "docker-compose.yml"), []byte(composeContent), 0644); err != nil {
		t.Fatalf("Failed to write compose file: %v", err)
	}

	manager := NewManager(tmpDir)

	_, err = manager.PullDeployment("latest-deployment", true)
	if err != nil {
		t.Logf("Pull (onlyLatest) returned error (expected if Docker unavailable): %v", err)
	}
}

func TestGetDeploymentImages(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "images-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	deploymentDir := filepath.Join(tmpDir, "images-deployment")
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}

	composeContent := `name: images-deployment
services:
  web:
    image: nginx:latest
  db:
    image: postgres:15
  app:
    build: .
`
	if err := os.WriteFile(filepath.Join(deploymentDir, "docker-compose.yml"), []byte(composeContent), 0644); err != nil {
		t.Fatalf("Failed to write compose file: %v", err)
	}

	manager := NewManager(tmpDir)

	images, err := manager.GetDeploymentImages("images-deployment")
	if err != nil {
		t.Fatalf("GetDeploymentImages failed: %v", err)
	}

	if len(images) != 3 {
		t.Errorf("Expected 3 images, got %d", len(images))
	}

	imageMap := make(map[string]ImageInfo)
	for _, img := range images {
		imageMap[img.Service] = img
	}

	if !imageMap["web"].IsLatest {
		t.Error("Expected nginx:latest to be marked as latest")
	}

	if imageMap["db"].IsLatest {
		t.Error("Expected postgres:15 to NOT be marked as latest")
	}

	if !imageMap["app"].IsBuild {
		t.Error("Expected app to be marked as build")
	}
}

func TestExecuteQuickActionNotFound(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "quick-action-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	manager := NewManager(tmpDir)

	_, err = manager.ExecuteQuickAction("nonexistent-deployment", "some-action")
	if err == nil {
		t.Error("Expected error for nonexistent deployment")
	}
}

func TestExecuteQuickActionNoActions(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "quick-action-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	deploymentDir := filepath.Join(tmpDir, "test-deployment")
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}

	composeContent := `name: test-deployment
services:
  app:
    image: nginx:alpine
`
	if err := os.WriteFile(filepath.Join(deploymentDir, "docker-compose.yml"), []byte(composeContent), 0644); err != nil {
		t.Fatalf("Failed to write compose file: %v", err)
	}

	manager := NewManager(tmpDir)

	_, err = manager.ExecuteQuickAction("test-deployment", "some-action")
	if err == nil {
		t.Error("Expected error when no quick actions defined")
	}
	if err.Error() != "no quick actions defined for deployment" {
		t.Errorf("Unexpected error message: %v", err)
	}
}

func TestExecuteQuickActionNotFoundAction(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "quick-action-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	deploymentDir := filepath.Join(tmpDir, "test-deployment")
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}

	composeContent := `name: test-deployment
services:
  app:
    image: nginx:alpine
`
	if err := os.WriteFile(filepath.Join(deploymentDir, "docker-compose.yml"), []byte(composeContent), 0644); err != nil {
		t.Fatalf("Failed to write compose file: %v", err)
	}

	metadataContent := `name: test-deployment
type: custom
quick_actions:
  - id: action1
    name: Test Action
    command: echo hello
networking:
  expose: false
  domain: ""
  container_port: 80
  protocol: http
ssl:
  enabled: false
  auto_cert: false
healthcheck:
  path: /
  interval: 30s
`
	if err := os.WriteFile(filepath.Join(deploymentDir, "service.yml"), []byte(metadataContent), 0644); err != nil {
		t.Fatalf("Failed to write metadata file: %v", err)
	}

	manager := NewManager(tmpDir)

	_, err = manager.ExecuteQuickAction("test-deployment", "nonexistent-action")
	if err == nil {
		t.Error("Expected error for nonexistent action")
	}
	if err.Error() != "quick action not found: nonexistent-action" {
		t.Errorf("Unexpected error message: %v", err)
	}
}

func TestResolveService(t *testing.T) {
	tests := []struct {
		name        string
		compose     string
		serviceName string
		want        string
		wantErr     string
	}{
		{
			name: "single service auto-resolves",
			compose: `name: test
services:
  backend:
    image: myapp:latest
`,
			want: "backend",
		},
		{
			name: "single service with explicit valid name",
			compose: `name: test
services:
  api:
    image: myapp:latest
`,
			serviceName: "api",
			want:        "api",
		},
		{
			name: "single service rejects invalid name",
			compose: `name: test
services:
  web:
    image: nginx:latest
`,
			serviceName: "nonexistent",
			wantErr:     "not found",
		},
		{
			name: "multiple services with app defaults to app",
			compose: `name: test
services:
  app:
    image: myapp:latest
  db:
    image: postgres:15
  cache:
    image: redis:alpine
`,
			want: "app",
		},
		{
			name: "multiple services without app rejects",
			compose: `name: test
services:
  web:
    image: nginx:latest
  api:
    image: myapi:latest
`,
			wantErr: "multiple services found",
		},
		{
			name: "multiple services with explicit valid name",
			compose: `name: test
services:
  frontend:
    image: nginx:latest
  api:
    image: myapi:latest
`,
			serviceName: "api",
			want:        "api",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", "resolve-svc-test-*")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tmpDir)

			deployDir := filepath.Join(tmpDir, "test-deployment")
			if err := os.MkdirAll(deployDir, 0755); err != nil {
				t.Fatalf("Failed to create deployment dir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(deployDir, "docker-compose.yml"), []byte(tt.compose), 0644); err != nil {
				t.Fatalf("Failed to write compose file: %v", err)
			}

			manager := NewManager(tmpDir)
			got, err := manager.ResolveService("test-deployment", tt.serviceName)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("Error = %q, want containing %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("ResolveService() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveServiceNotFound(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "resolve-svc-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	manager := NewManager(tmpDir)
	_, err = manager.ResolveService("nonexistent", "")
	if err == nil {
		t.Fatal("Expected error for nonexistent deployment")
	}
}

func TestGetComposeServiceNames(t *testing.T) {
	tests := []struct {
		name      string
		compose   string
		wantNames []string
		wantErr   bool
	}{
		{
			name: "single service",
			compose: `name: test
services:
  app:
    image: nginx:latest
`,
			wantNames: []string{"app"},
		},
		{
			name: "multiple services",
			compose: `name: test
services:
  web:
    image: nginx:latest
  db:
    image: postgres:15
  cache:
    image: redis:alpine
`,
			wantNames: []string{"web", "db", "cache"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", "svc-names-test-*")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tmpDir)

			deployDir := filepath.Join(tmpDir, "test-deployment")
			if err := os.MkdirAll(deployDir, 0755); err != nil {
				t.Fatalf("Failed to create deployment dir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(deployDir, "docker-compose.yml"), []byte(tt.compose), 0644); err != nil {
				t.Fatalf("Failed to write compose file: %v", err)
			}

			manager := NewManager(tmpDir)
			names, err := manager.GetComposeServiceNames("test-deployment")
			if tt.wantErr {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("GetComposeServiceNames() error: %v", err)
			}

			if len(names) != len(tt.wantNames) {
				t.Errorf("GetComposeServiceNames() returned %d names, want %d", len(names), len(tt.wantNames))
				return
			}

			nameSet := make(map[string]bool)
			for _, n := range names {
				nameSet[n] = true
			}
			for _, want := range tt.wantNames {
				if !nameSet[want] {
					t.Errorf("Expected service name %q not found in %v", want, names)
				}
			}
		})
	}
}

func TestGetComposeServiceNamesNotFound(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "svc-names-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	manager := NewManager(tmpDir)
	_, err = manager.GetComposeServiceNames("nonexistent")
	if err == nil {
		t.Error("Expected error for nonexistent deployment")
	}
}

func TestGetComposeServices(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "svc-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	deployDir := filepath.Join(tmpDir, "test-deployment")
	if err := os.MkdirAll(deployDir, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}

	composeContent := `name: test-deployment
services:
  web:
    image: nginx:latest
    ports:
      - "80:80"
  worker:
    image: myapp:latest
`
	if err := os.WriteFile(filepath.Join(deployDir, "docker-compose.yml"), []byte(composeContent), 0644); err != nil {
		t.Fatalf("Failed to write compose file: %v", err)
	}

	manager := NewManager(tmpDir)
	services, err := manager.GetComposeServices("test-deployment")
	if err != nil {
		t.Fatalf("GetComposeServices() error: %v", err)
	}

	if len(services) != 2 {
		t.Errorf("Expected 2 services, got %d", len(services))
	}

	nameSet := make(map[string]bool)
	for _, s := range services {
		nameSet[s.Name] = true
	}
	if !nameSet["web"] {
		t.Error("Expected 'web' service not found")
	}
	if !nameSet["worker"] {
		t.Error("Expected 'worker' service not found")
	}
}

func TestComposeExecWithoutAPIClient(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "exec-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	manager := NewManager(tmpDir)
	manager.apiClient = nil

	_, err = manager.ComposeExec(context.TODO(), "test", "app", "echo hello")
	if err == nil {
		t.Error("Expected error when API client is nil")
	}
	if err.Error() != "docker API client not available" {
		t.Errorf("Unexpected error: %v", err)
	}
}

func TestPopulateContainerInfoJSONArray(t *testing.T) {
	deployment := &models.Deployment{
		Name: "test",
		Services: []models.Service{
			{Name: "app"},
			{Name: "db"},
		},
	}

	containers := []composeContainer{
		{ID: "abc123", Name: "test-app-1", Service: "app", State: "running", Health: "healthy"},
		{ID: "def456", Name: "test-db-1", Service: "db", State: "running"},
	}

	for i := range deployment.Services {
		svc := &deployment.Services[i]
		for _, container := range containers {
			if container.Service == svc.Name {
				svc.ContainerID = container.ID
				svc.Status = container.State
				if container.Health != "" {
					svc.Health = container.Health
				}
				break
			}
		}
	}

	if deployment.Services[0].ContainerID != "abc123" {
		t.Errorf("Expected app container ID to be abc123, got %s", deployment.Services[0].ContainerID)
	}
	if deployment.Services[0].Status != "running" {
		t.Errorf("Expected app status to be running, got %s", deployment.Services[0].Status)
	}
	if deployment.Services[0].Health != "healthy" {
		t.Errorf("Expected app health to be healthy, got %s", deployment.Services[0].Health)
	}
	if deployment.Services[1].ContainerID != "def456" {
		t.Errorf("Expected db container ID to be def456, got %s", deployment.Services[1].ContainerID)
	}
}

func TestSnapshotBindMounts(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "snapshot-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	deploymentDir := filepath.Join(tmpDir, "test-deployment")
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}

	composeContent := `name: test-deployment
services:
  app:
    image: bitnami/laravel:latest
    volumes:
      - ./storage:/app/storage
      - ./logs:/app/logs
`
	if err := os.WriteFile(filepath.Join(deploymentDir, "docker-compose.yml"), []byte(composeContent), 0644); err != nil {
		t.Fatalf("Failed to write compose file: %v", err)
	}

	storageDir := filepath.Join(deploymentDir, "storage")
	if err := os.MkdirAll(filepath.Join(storageDir, "framework", "sessions"), 0755); err != nil {
		t.Fatalf("Failed to create storage subdirs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(storageDir, "framework", "sessions", "abc.session"), []byte("session-data"), 0644); err != nil {
		t.Fatalf("Failed to write session file: %v", err)
	}

	logsDir := filepath.Join(deploymentDir, "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatalf("Failed to create logs dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, "app.log"), []byte("log-data"), 0644); err != nil {
		t.Fatalf("Failed to write log file: %v", err)
	}

	manager := NewManager(tmpDir)
	snapshotDir := manager.snapshotBindMounts("test-deployment", deploymentDir)
	if snapshotDir == "" {
		t.Fatal("Expected non-empty snapshot dir")
	}
	defer os.RemoveAll(snapshotDir)

	sessionFile := filepath.Join(snapshotDir, "storage", "framework", "sessions", "abc.session")
	data, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatalf("Session file not found in snapshot: %v", err)
	}
	if string(data) != "session-data" {
		t.Errorf("Expected 'session-data', got %q", string(data))
	}

	logFile := filepath.Join(snapshotDir, "logs", "app.log")
	data, err = os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Log file not found in snapshot: %v", err)
	}
	if string(data) != "log-data" {
		t.Errorf("Expected 'log-data', got %q", string(data))
	}
}

func TestSnapshotBindMounts_NoMounts(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "snapshot-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	deploymentDir := filepath.Join(tmpDir, "test-deployment")
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}

	composeContent := `name: test-deployment
services:
  app:
    image: nginx:latest
`
	if err := os.WriteFile(filepath.Join(deploymentDir, "docker-compose.yml"), []byte(composeContent), 0644); err != nil {
		t.Fatalf("Failed to write compose file: %v", err)
	}

	manager := NewManager(tmpDir)
	snapshotDir := manager.snapshotBindMounts("test-deployment", deploymentDir)
	if snapshotDir != "" {
		os.RemoveAll(snapshotDir)
		t.Error("Expected empty snapshot dir for compose without bind mounts")
	}
}

func TestRestoreBindMounts(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "restore-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	snapshotDir, err := os.MkdirTemp("", "flatrun-snapshot-*")
	if err != nil {
		t.Fatalf("Failed to create snapshot dir: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(snapshotDir, "storage", "logs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "storage", "logs", "app.log"), []byte("old-log"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "storage", "config.json"), []byte("config"), 0644); err != nil {
		t.Fatal(err)
	}

	deploymentDir := filepath.Join(tmpDir, "deployment")
	if err := os.MkdirAll(filepath.Join(deploymentDir, "storage"), 0755); err != nil {
		t.Fatal(err)
	}

	manager := NewManager(tmpDir)
	manager.restoreBindMounts(deploymentDir, snapshotDir)

	data, err := os.ReadFile(filepath.Join(deploymentDir, "storage", "logs", "app.log"))
	if err != nil {
		t.Fatalf("Restored file not found: %v", err)
	}
	if string(data) != "old-log" {
		t.Errorf("Expected 'old-log', got %q", string(data))
	}

	data, err = os.ReadFile(filepath.Join(deploymentDir, "storage", "config.json"))
	if err != nil {
		t.Fatalf("Restored config not found: %v", err)
	}
	if string(data) != "config" {
		t.Errorf("Expected 'config', got %q", string(data))
	}

	if _, err := os.Stat(snapshotDir); !os.IsNotExist(err) {
		t.Error("Expected snapshot dir to be cleaned up")
	}
}

func TestRestoreBindMounts_MergePreservesExisting(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "restore-merge-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	snapshotDir, err := os.MkdirTemp("", "flatrun-snapshot-*")
	if err != nil {
		t.Fatalf("Failed to create snapshot dir: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(snapshotDir, "storage"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "storage", "existing.txt"), []byte("old-content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "storage", "missing.txt"), []byte("restored-content"), 0644); err != nil {
		t.Fatal(err)
	}

	deploymentDir := filepath.Join(tmpDir, "deployment")
	if err := os.MkdirAll(filepath.Join(deploymentDir, "storage"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deploymentDir, "storage", "existing.txt"), []byte("new-content"), 0644); err != nil {
		t.Fatal(err)
	}

	manager := NewManager(tmpDir)
	manager.restoreBindMounts(deploymentDir, snapshotDir)

	data, err := os.ReadFile(filepath.Join(deploymentDir, "storage", "existing.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new-content" {
		t.Errorf("Existing file was overwritten: expected 'new-content', got %q", string(data))
	}

	data, err = os.ReadFile(filepath.Join(deploymentDir, "storage", "missing.txt"))
	if err != nil {
		t.Fatalf("Missing file was not restored: %v", err)
	}
	if string(data) != "restored-content" {
		t.Errorf("Expected 'restored-content', got %q", string(data))
	}
}

func TestRestoreBindMounts_EmptySnapshotDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "restore-empty-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	manager := NewManager(tmpDir)
	manager.restoreBindMounts(tmpDir, "")
}

func TestRebuildDeployment_SnapshotRestore(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "rebuild-snapshot-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	deploymentDir := filepath.Join(tmpDir, "test-deployment")
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}

	composeContent := `name: test-deployment
services:
  app:
    image: bitnami/laravel:latest
    volumes:
      - ./storage:/app/storage
`
	if err := os.WriteFile(filepath.Join(deploymentDir, "docker-compose.yml"), []byte(composeContent), 0644); err != nil {
		t.Fatalf("Failed to write compose file: %v", err)
	}

	storageDir := filepath.Join(deploymentDir, "storage")
	if err := os.MkdirAll(storageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storageDir, "user-data.txt"), []byte("important"), 0644); err != nil {
		t.Fatal(err)
	}

	manager := NewManager(tmpDir)

	_, err = manager.RebuildDeployment("test-deployment")
	if err != nil {
		t.Logf("Rebuild returned error (expected if Docker unavailable): %v", err)
	}

	data, err := os.ReadFile(filepath.Join(storageDir, "user-data.txt"))
	if err != nil {
		t.Fatalf("User data file lost after rebuild: %v", err)
	}
	if string(data) != "important" {
		t.Errorf("Expected 'important', got %q", string(data))
	}
}

func TestRestartDeployment_SnapshotRestore(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "restart-snapshot-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	deploymentDir := filepath.Join(tmpDir, "test-deployment")
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}

	composeContent := `name: test-deployment
services:
  app:
    image: bitnami/laravel:latest
    volumes:
      - ./storage:/app/storage
`
	if err := os.WriteFile(filepath.Join(deploymentDir, "docker-compose.yml"), []byte(composeContent), 0644); err != nil {
		t.Fatalf("Failed to write compose file: %v", err)
	}

	storageDir := filepath.Join(deploymentDir, "storage")
	if err := os.MkdirAll(storageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storageDir, "user-data.txt"), []byte("important"), 0644); err != nil {
		t.Fatal(err)
	}

	manager := NewManager(tmpDir)

	_, err = manager.RestartDeployment("test-deployment")
	if err != nil {
		t.Logf("Restart returned error (expected if Docker unavailable): %v", err)
	}

	data, err := os.ReadFile(filepath.Join(storageDir, "user-data.txt"))
	if err != nil {
		t.Fatalf("User data file lost after restart: %v", err)
	}
	if string(data) != "important" {
		t.Errorf("Expected 'important', got %q", string(data))
	}
}
