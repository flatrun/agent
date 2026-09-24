package api

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/docker"
)

func TestContainerDeploymentNameUsesOwningDirectory(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is unavailable")
	}
	image := os.Getenv("FLATRUN_DOCKER_TEST_IMAGE")
	if image == "" {
		image = "busybox:latest"
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Skipf("docker test image %s is unavailable", image)
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	deploymentName := "actual-" + suffix
	projectName := "label-" + suffix
	containerName := "container-" + suffix
	root := t.TempDir()
	deploymentDir := filepath.Join(root, deploymentName)
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		t.Fatal(err)
	}
	compose := "name: " + projectName + "\nservices:\n  web:\n    image: " + image + "\n"
	if err := os.WriteFile(filepath.Join(deploymentDir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}

	output, err := exec.Command("docker", "create",
		"--name", containerName,
		"--label", composeProjectLabel+"="+projectName,
		"--label", "com.docker.compose.service=web",
		image, "sleep", "60").CombinedOutput()
	if err != nil {
		t.Fatalf("create container: %v: %s", err, output)
	}
	containerID := strings.TrimSpace(string(output))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", containerID).Run() })
	if output, err := exec.Command("docker", "start", containerID).CombinedOutput(); err != nil {
		t.Fatalf("start container: %v: %s", err, output)
	}

	server := &Server{manager: docker.NewManager(root)}
	resolved, err := server.containerDeploymentName(containerID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != deploymentName {
		t.Fatalf("deployment = %q, want %q", resolved, deploymentName)
	}
	resolved, err = server.containerDeploymentName(containerName)
	if err != nil || resolved != deploymentName {
		t.Fatalf("deployment by name = %q, error = %v", resolved, err)
	}
}
