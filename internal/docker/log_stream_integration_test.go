package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestStreamLogsDeliversLinesAsTheyAreWritten is the point of following rather than tailing:
// a line written after the viewer attached still reaches it, without asking again.
func TestStreamLogsDeliversLinesAsTheyAreWritten(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real container")
	}

	const name = "flatrun-logstream-integration"
	base := t.TempDir()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	// A container that keeps printing, so there is always a next line to wait for.
	compose := "name: " + name + `
services:
  app:
    image: nginx:alpine
    entrypoint: ["/bin/sh", "-c", "i=0; while true; do i=$$((i+1)); echo flatrun-line-$$i; sleep 1; done"]
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var (
		mu    sync.Mutex
		lines []string
	)
	got := make(chan struct{})
	var once sync.Once

	go func() {
		_ = m.StreamDeploymentLogs(ctx, name, dir, 10, func(line string) {
			mu.Lock()
			lines = append(lines, line)
			n := len(lines)
			mu.Unlock()
			// Wait for a few, so this cannot pass on the backlog alone.
			if n >= 3 {
				once.Do(func() { close(got) })
			}
		})
	}()

	select {
	case <-got:
	case <-ctx.Done():
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("only received %d lines while following: %v", len(lines), lines)
	}

	mu.Lock()
	defer mu.Unlock()
	var found bool
	for _, l := range lines {
		if strings.Contains(l, "flatrun-line-") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("none of the lines came from the container: %v", lines)
	}
}

// TestStreamLogsStopsWhenTheViewerLeaves pins that cancelling ends the follow, rather than
// leaving a compose process attached for the life of the agent.
func TestStreamLogsStopsWhenTheViewerLeaves(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real container")
	}

	const name = "flatrun-logstream-cancel"
	base := t.TempDir()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	compose := "name: " + name + `
services:
  app:
    image: nginx:alpine
    entrypoint: ["/bin/sh", "-c", "while true; do echo tick; sleep 1; done"]
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

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- m.StreamDeploymentLogs(ctx, name, dir, 5, func(string) {})
	}()

	time.Sleep(2 * time.Second)
	cancel()

	select {
	case err := <-done:
		// A cancelled follow is the viewer leaving, not a failure to report.
		if err != nil {
			t.Errorf("cancelling the follow reported an error: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("following did not stop when the viewer left")
	}
}
