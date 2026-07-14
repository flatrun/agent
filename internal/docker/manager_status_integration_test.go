package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/flatrun/agent/pkg/models"
)

// TestDeploymentStatusAgainstRealContainers pins the status semantics the
// compose CLI path defined, which the engine query has to reproduce exactly.
//
// The stopped half is the load-bearing one: compose reports only live
// containers, so a stopped deployment must read as stopped and expose no
// container. Widening the engine query to every container would report it as
// unknown with a container attached instead, which no unit test would catch.
func TestDeploymentStatusAgainstRealContainers(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real container")
	}

	const name = "flatrun-status-integration"
	base := t.TempDir()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	// nginx:alpine is already used by the compose tests in this package. It has
	// no healthcheck, so health stays empty here and is covered by unit tests.
	compose := "name: " + name + `
services:
  app:
    image: nginx:alpine
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(base)
	if m.apiClient == nil {
		t.Skip("docker api client unavailable")
	}

	// Building the client does not contact the daemon, so reach it once here.
	// Everything after this point is a real failure rather than a reason to
	// skip, which keeps a broken deployment path from passing as "no docker".
	ctx, cancel := context.WithTimeout(context.Background(), statusReadTimeout)
	defer cancel()
	if _, err := m.apiClient.ListLiveComposeContainers(ctx); err != nil {
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

	statusOf := func(t *testing.T) string {
		t.Helper()
		deployments, err := m.ListDeployments()
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range deployments {
			if d.Name == name {
				return d.Status
			}
		}
		t.Fatalf("deployment %q missing from the list", name)
		return ""
	}

	if got := statusOf(t); got != string(models.StatusRunning) {
		t.Errorf("running deployment listed as %q, want running", got)
	}

	running, err := m.GetDeployment(name)
	if err != nil {
		t.Fatal(err)
	}
	if got := running.Status; got != string(models.StatusRunning) {
		t.Errorf("running deployment detail status = %q, want running", got)
	}
	if len(running.Services) != 1 {
		t.Fatalf("got %d services, want 1", len(running.Services))
	}
	app := running.Services[0]
	if app.Status != "running" {
		t.Errorf("app service status = %q, want running", app.Status)
	}
	if len(app.ContainerID) != 12 {
		t.Errorf("app container id = %q, want Docker's 12-character short form", app.ContainerID)
	}

	if out, err := m.StopDeployment(name); err != nil {
		t.Fatalf("stop failed: %v (%s)", err, out)
	}

	if got := statusOf(t); got != string(models.StatusStopped) {
		t.Errorf("stopped deployment listed as %q, want stopped", got)
	}

	stopped, err := m.GetDeployment(name)
	if err != nil {
		t.Fatal(err)
	}
	if got := stopped.Status; got != string(models.StatusStopped) {
		t.Errorf("stopped deployment detail status = %q, want stopped", got)
	}
	if got := stopped.Services[0].ContainerID; got != "" {
		t.Errorf("stopped service exposes container %q, want none", got)
	}
}
