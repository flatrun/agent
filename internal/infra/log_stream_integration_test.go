package infra

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flatrun/agent/pkg/config"
)

func startTalkingContainer(t *testing.T, name, command string) {
	t.Helper()

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker unavailable")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("docker daemon unreachable: %v", err)
	}

	_ = exec.Command("docker", "rm", "-f", name).Run()
	out, err := exec.Command("docker", "run", "-d", "--name", name, "alpine:latest", "/bin/sh", "-c", command).CombinedOutput()
	if err != nil {
		t.Fatalf("starting the container failed: %v: %s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("docker", "rm", "-f", name).CombinedOutput(); err != nil {
			t.Logf("cleanup: %v: %s", err, out)
		}
	})
}

// Splitting a container's two outputs is what lets the proxy's access log and error log be
// read apart, so it has to hold against a real container rather than in principle.
func TestServiceLogsReadEachOutputApart(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real container")
	}

	const name = "flatrun-infra-logsplit"
	startTalkingContainer(t, name, "echo an-access-line; echo an-error-line >&2; sleep 60")

	m := NewManager(&config.Config{})

	var all string
	for attempt := 0; attempt < 15; attempt++ {
		var err error
		all, err = m.ServiceLogs(name, 100, LogStreamAll)
		if err != nil {
			t.Fatalf("reading logs failed: %v", err)
		}
		if strings.Contains(all, "an-access-line") && strings.Contains(all, "an-error-line") {
			break
		}
		time.Sleep(time.Second)
	}
	if !strings.Contains(all, "an-access-line") || !strings.Contains(all, "an-error-line") {
		t.Fatalf("expected both outputs together, got: %s", all)
	}

	stdout, err := m.ServiceLogs(name, 100, LogStreamStdout)
	if err != nil {
		t.Fatalf("reading stdout failed: %v", err)
	}
	if !strings.Contains(stdout, "an-access-line") || strings.Contains(stdout, "an-error-line") {
		t.Errorf("stdout should carry only what the container printed there, got: %s", stdout)
	}

	stderr, err := m.ServiceLogs(name, 100, LogStreamStderr)
	if err != nil {
		t.Fatalf("reading stderr failed: %v", err)
	}
	if !strings.Contains(stderr, "an-error-line") || strings.Contains(stderr, "an-access-line") {
		t.Errorf("stderr should carry only what the container printed there, got: %s", stderr)
	}
}

// Following one output has to keep delivering, which is what the viewer's Follow depends on.
func TestStreamServiceLogsFollowsOneOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real container")
	}

	const name = "flatrun-infra-logfollow"
	startTalkingContainer(t, name, "i=0; while true; do i=$((i+1)); echo out-$i; echo err-$i >&2; sleep 1; done")

	m := NewManager(&config.Config{})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var (
		mu    sync.Mutex
		lines []string
	)
	got := make(chan struct{})
	var once sync.Once

	go func() {
		_ = m.StreamServiceLogs(ctx, name, 10, LogStreamStdout, func(line string) {
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
	for _, line := range lines {
		if strings.Contains(line, "err-") {
			t.Errorf("following stdout delivered a line from the other output: %q", line)
		}
	}
}
