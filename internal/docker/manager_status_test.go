package docker

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/flatrun/agent/pkg/models"
)

// The State and Status pairs below are values the Docker daemon actually
// reports: "Up 2 hours (healthy)" and "Exited (1) 13 hours ago" were captured
// from `docker ps -a` against a live daemon, and "(health: starting)" is the
// daemon's own rendering of a starting health check.
func summary(state container.ContainerState, status, service string) container.Summary {
	return container.Summary{
		ID:     "ea80e6851a168fbe35db7e9a82362357305d4e0b948c55af789d6ceff92fe6d3",
		State:  state,
		Status: status,
		Labels: map[string]string{
			composeProjectLabel: "trakli",
			composeServiceLabel: service,
		},
	}
}

func TestStatusFromContainers(t *testing.T) {
	tests := []struct {
		name       string
		containers []container.Summary
		want       string
	}{
		{
			// A stopped deployment's containers are excluded from the index, so
			// it reaches this function with none.
			name: "no containers is stopped",
			want: string(models.StatusStopped),
		},
		{
			name:       "any running container is running",
			containers: []container.Summary{summary(container.StateRestarting, "Restarting (1) 3 seconds ago", "worker"), summary(container.StateRunning, "Up 2 hours", "app")},
			want:       string(models.StatusRunning),
		},
		{
			name:       "paused is paused",
			containers: []container.Summary{summary(container.StatePaused, "Up 2 hours (Paused)", "app")},
			want:       string(models.StatusPaused),
		},
		{
			name: "running takes precedence over paused",
			containers: []container.Summary{
				summary(container.StatePaused, "Up 2 hours (Paused)", "worker"),
				summary(container.StateRunning, "Up 2 hours", "app"),
			},
			want: string(models.StatusRunning),
		},
		{
			name:       "live but nothing up is unknown",
			containers: []container.Summary{summary(container.StateRestarting, "Restarting (1) 3 seconds ago", "app")},
			want:       string(models.StatusUnknown),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusFromContainers(tt.containers); got != tt.want {
				t.Errorf("statusFromContainers() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHealthFromStatus(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"Up 2 hours (healthy)", "healthy"},
		{"Up 2 hours (unhealthy)", "unhealthy"},
		{"Up 2 seconds (health: starting)", "starting"},
		{"Up 2 hours", ""},
		{"Exited (1) 13 hours ago", ""},
		{"Created", ""},
		// A paused container's parenthesised suffix is not a health report.
		{"Up 2 hours (Paused)", ""},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			if got := healthFromStatus(tt.status); got != tt.want {
				t.Errorf("healthFromStatus(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestApplyContainers(t *testing.T) {
	deployment := &models.Deployment{
		Name: "trakli",
		Services: []models.Service{
			{Name: "app"},
			{Name: "mysql"},
			{Name: "absent"},
		},
	}

	applyContainers(deployment, []container.Summary{
		summary(container.StateRunning, "Up 2 days", "app"),
		summary(container.StateRunning, "Up 2 days (healthy)", "mysql"),
	})

	if deployment.Status != string(models.StatusRunning) {
		t.Errorf("Status = %q, want running", deployment.Status)
	}
	if got := deployment.Services[0].Status; got != container.StateRunning {
		t.Errorf("app Status = %q, want running", got)
	}
	// Clients already receive Docker's short ID from the compose CLI, so the
	// Engine API's full ID is trimmed to match.
	if got := deployment.Services[0].ContainerID; got != "ea80e6851a16" {
		t.Errorf("app ContainerID = %q, want ea80e6851a16", got)
	}
	if got := deployment.Services[0].Health; got != "" {
		t.Errorf("app Health = %q, want empty", got)
	}
	if got := deployment.Services[1].Health; got != "healthy" {
		t.Errorf("mysql Health = %q, want healthy", got)
	}
	// A service with no container keeps its zero values rather than borrowing
	// another service's.
	if got := deployment.Services[2].ContainerID; got != "" {
		t.Errorf("absent ContainerID = %q, want empty", got)
	}
}

// writeDeployment creates a deployment directory whose compose file optionally
// carries an explicit project `name:`.
func writeDeployment(t *testing.T, base, name string, named bool) {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	compose := "services:\n  app:\n    image: nginx:alpine\n"
	if named {
		compose = "name: " + name + "\n" + compose
	}
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestStatusFallbackLogsOncePerOutage(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	m := &Manager{}
	failure := errors.New("daemon is not running")
	for i := 0; i < 3; i++ {
		m.noteStatusFallback(failure)
	}
	if got := strings.Count(logged.String(), "falling back"); got != 1 {
		t.Errorf("logged the fallback %d times across 3 failed reads, want 1", got)
	}

	// Recovering and failing again is a new outage, and worth a line.
	m.noteStatusRecovered()
	m.noteStatusRecovered()
	if got := strings.Count(logged.String(), "reachable again"); got != 1 {
		t.Errorf("logged recovery %d times, want 1", got)
	}

	m.noteStatusFallback(failure)
	if got := strings.Count(logged.String(), "falling back"); got != 2 {
		t.Errorf("logged the fallback %d times across two outages, want 2", got)
	}
}

func TestProjectForPrefersComposeName(t *testing.T) {
	base := t.TempDir()
	writeDeployment(t, base, "shop", true)

	m := NewManager(base)
	deps, err := m.discovery.FindDeployments()
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 1 {
		t.Fatalf("got %d deployments, want 1", len(deps))
	}

	if got := m.projectFor(&deps[0], containerIndex{}); got != "shop" {
		t.Errorf("projectFor() = %q, want shop", got)
	}
}

func TestProjectForFallsBackToIndex(t *testing.T) {
	base := t.TempDir()
	writeDeployment(t, base, "imported", false)

	m := NewManager(base)
	deps, err := m.discovery.FindDeployments()
	if err != nil {
		t.Fatal(err)
	}

	// No compose `name:`, so the project is detected from containers already on
	// the host, matching the prefixed name the CLI probe would have found.
	index := containerIndex{"flatrun-imported": []container.Summary{summary(container.StateRunning, "Up 2 hours", "app")}}
	if got := m.projectFor(&deps[0], index); got != "flatrun-imported" {
		t.Errorf("projectFor() = %q, want flatrun-imported", got)
	}

	// With nothing on the host, it falls back to the directory name.
	if got := m.projectFor(&deps[0], containerIndex{}); got != "imported" {
		t.Errorf("projectFor() = %q, want imported", got)
	}
}
