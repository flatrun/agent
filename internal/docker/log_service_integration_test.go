package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLogsNarrowToOneService is the point of the service filter: a deployment with two noisy
// containers hands back only the one the viewer picked.
func TestLogsNarrowToOneService(t *testing.T) {
	if testing.Short() {
		t.Skip("starts real containers")
	}

	const name = "flatrun-logservice-integration"
	base := t.TempDir()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	compose := "name: " + name + `
services:
  web:
    image: alpine:latest
    entrypoint: ["/bin/sh", "-c", "echo web-speaking; sleep 300"]
  worker:
    image: alpine:latest
    entrypoint: ["/bin/sh", "-c", "echo worker-speaking; sleep 300"]
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(base)
	if m.apiClient == nil {
		t.Skip("docker api client unavailable")
	}
	probe, cancelProbe := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelProbe()
	if _, err := m.apiClient.ListLiveComposeContainers(probe); err != nil {
		t.Skipf("docker daemon unreachable: %v", err)
	}
	t.Cleanup(func() {
		if _, err := m.executor.Down(dir); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})
	if out, err := m.StartDeployment(name); err != nil {
		t.Fatalf("start failed: %v (%s)", err, out)
	}

	all, err := m.GetDeploymentLogs(name, 100)
	if err != nil {
		t.Fatalf("reading every service failed: %v", err)
	}
	if !strings.Contains(all, "web-speaking") || !strings.Contains(all, "worker-speaking") {
		t.Fatalf("expected both services without a filter, got: %s", all)
	}

	only, err := m.GetDeploymentLogs(name, 100, "worker")
	if err != nil {
		t.Fatalf("reading one service failed: %v", err)
	}
	if !strings.Contains(only, "worker-speaking") {
		t.Errorf("expected the picked service's output, got: %s", only)
	}
	if strings.Contains(only, "web-speaking") {
		t.Errorf("expected no output from the other service, got: %s", only)
	}
}
