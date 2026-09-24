package networks

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestEnsureContainerOnNetworkAcceptsNameAndID(t *testing.T) {
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
	networkName := "flatrun-network-test-" + suffix
	containerName := "flatrun-container-test-" + suffix
	if output, err := exec.Command("docker", "network", "create", networkName).CombinedOutput(); err != nil {
		t.Fatalf("create network: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", networkName).Run() })

	output, err := exec.Command("docker", "create", "--name", containerName, image, "sleep", "60").CombinedOutput()
	if err != nil {
		t.Fatalf("create container: %v: %s", err, output)
	}
	containerID := strings.TrimSpace(string(output))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", containerID).Run() })

	manager := NewManager()
	if err := manager.EnsureContainerOnNetwork(networkName, containerName); err != nil {
		t.Fatalf("attach by name: %v", err)
	}
	if err := manager.EnsureContainerOnNetwork(networkName, containerID); err != nil {
		t.Fatalf("repeat attachment by ID: %v", err)
	}
	if !manager.IsContainerOnNetwork(networkName, containerName) || !manager.IsContainerOnNetwork(networkName, containerID) {
		t.Fatal("container was not attached for both identifiers")
	}
}
