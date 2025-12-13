package docker

import (
	"os"
	"path/filepath"
	"testing"
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
