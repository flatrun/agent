package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyUpFlags(t *testing.T) {
	base := []string{"up", "-d", "--remove-orphans"}

	if got := applyUpFlags(base, runOpts{}); strings.Join(got, " ") != "up -d --remove-orphans" {
		t.Errorf("no options should not add flags, got %v", got)
	}

	got := applyUpFlags(base, runOpts{forceRecreate: true, freshPull: true})
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--force-recreate") {
		t.Errorf("forceRecreate should add --force-recreate, got %v", got)
	}
	if !strings.Contains(joined, "--pull always") {
		t.Errorf("freshPull should add --pull always, got %v", got)
	}
}

func TestResolveRunOpts(t *testing.T) {
	ro := resolveRunOpts([]RunOption{WithForceRecreate(), WithNoCache(), WithFreshPull()})
	if !ro.forceRecreate || !ro.noCache || !ro.freshPull {
		t.Errorf("resolveRunOpts did not set all flags: %+v", ro)
	}
}

func TestComposeExecutorPull(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "compose-pull-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	deploymentDir := filepath.Join(tmpDir, "test-app")
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}

	composeContent := `name: test-app
services:
  web:
    image: nginx:alpine
`
	if err := os.WriteFile(filepath.Join(deploymentDir, "docker-compose.yml"), []byte(composeContent), 0644); err != nil {
		t.Fatalf("Failed to write compose file: %v", err)
	}

	executor := NewComposeExecutor(tmpDir)

	output, err := executor.Pull(deploymentDir, false)
	if err != nil {
		t.Logf("Pull returned error (expected if Docker unavailable): %v, output: %s", err, output)
	}
}

func TestComposeExecutorPullMixedServices(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "compose-pull-mixed-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	deploymentDir := filepath.Join(tmpDir, "mixed-app")
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}

	composeContent := `name: mixed-app
services:
  app:
    build: .
  db:
    image: postgres:15-alpine
  cache:
    image: redis:alpine
`
	if err := os.WriteFile(filepath.Join(deploymentDir, "docker-compose.yml"), []byte(composeContent), 0644); err != nil {
		t.Fatalf("Failed to write compose file: %v", err)
	}

	if err := os.WriteFile(filepath.Join(deploymentDir, "Dockerfile"), []byte("FROM alpine\n"), 0644); err != nil {
		t.Fatalf("Failed to write Dockerfile: %v", err)
	}

	executor := NewComposeExecutor(tmpDir)

	output, err := executor.Pull(deploymentDir, false)
	if err != nil {
		t.Logf("Pull returned error (expected if Docker unavailable): %v, output: %s", err, output)
	}
}

func TestGetImageInfo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "image-info-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	deploymentDir := filepath.Join(tmpDir, "test-app")
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}

	composeContent := `name: test-app
services:
  web:
    image: ${WEB_IMAGE:-nginx:latest}
  db:
    image: postgres:15
  cache:
    image: redis
  app:
    build: .
`
	if err := os.WriteFile(filepath.Join(deploymentDir, "docker-compose.yml"), []byte(composeContent), 0644); err != nil {
		t.Fatalf("Failed to write compose file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deploymentDir, ".env"), []byte("WEB_IMAGE=nginx:1.27\n"), 0600); err != nil {
		t.Fatalf("Failed to write environment file: %v", err)
	}

	executor := NewComposeExecutor(tmpDir)

	images, err := executor.GetImageInfo(deploymentDir)
	if err != nil {
		t.Fatalf("GetImageInfo failed: %v", err)
	}

	if len(images) != 4 {
		t.Errorf("Expected 4 images, got %d", len(images))
	}

	imageMap := make(map[string]ImageInfo)
	for _, img := range images {
		imageMap[img.Service] = img
	}

	if imageMap["web"].Image != "nginx:1.27" || imageMap["web"].SourceImage != "${WEB_IMAGE:-nginx:latest}" || !imageMap["web"].Resolved {
		t.Errorf("Expected the web image to be resolved with its source retained, got %+v", imageMap["web"])
	}

	if imageMap["web"].IsLatest {
		t.Error("Expected nginx:1.27 to NOT be marked as latest")
	}

	if imageMap["db"].IsLatest {
		t.Error("Expected postgres:15 to NOT be marked as latest")
	}

	if !imageMap["cache"].IsLatest {
		t.Error("Expected redis (no tag) to be marked as latest")
	}

	if !imageMap["app"].IsBuild {
		t.Error("Expected app to be marked as build")
	}
}

func TestIsLatestTag(t *testing.T) {
	tests := []struct {
		image    string
		expected bool
	}{
		{"nginx:latest", true},
		{"nginx", true},
		{"nginx:1.25.3", false},
		{"postgres:15-alpine", false},
		{"redis:alpine", false},
		{"myregistry.com/image:latest", true},
		{"myregistry.com/image", true},
		{"myregistry.com/image:v1.0.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			result := isLatestTag(tt.image)
			if result != tt.expected {
				t.Errorf("isLatestTag(%q) = %v, expected %v", tt.image, result, tt.expected)
			}
		})
	}
}

func TestPullOnlyLatest(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pull-latest-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	deploymentDir := filepath.Join(tmpDir, "test-app")
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}

	composeContent := `name: test-app
services:
  web:
    image: nginx:latest
  db:
    image: postgres:15
`
	if err := os.WriteFile(filepath.Join(deploymentDir, "docker-compose.yml"), []byte(composeContent), 0644); err != nil {
		t.Fatalf("Failed to write compose file: %v", err)
	}

	executor := NewComposeExecutor(tmpDir)

	output, err := executor.Pull(deploymentDir, true)
	if err != nil {
		t.Logf("Pull (onlyLatest) returned error (expected if Docker unavailable): %v, output: %s", err, output)
	}
}
