package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestUnmountDetachesTheContainerFromTheHost pins the rule the UI warns about:
// while a path is mounted the host copy is what the container reads, so deleting
// it there takes it from the container too; once unmounted the container is back
// on its image's content and the host copy cannot reach it.
func TestUnmountDetachesTheContainerFromTheHost(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real container")
	}

	const (
		name    = "flatrun-unmount-integration"
		service = "app"
		marker  = "/etc/nginx/conf.d/marker.conf"
	)

	base := t.TempDir()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
	if _, err := m.apiClient.ExecInService(ctx, name, service, "echo '# marker' > "+marker); err != nil {
		t.Fatal(err)
	}
	if err := m.MaterializeMount(name, service, "/etc/nginx/conf.d", "./conf.d"); err != nil {
		t.Fatal(err)
	}

	hostMarker := filepath.Join(dir, "conf.d", "marker.conf")
	if _, err := os.Stat(hostMarker); err != nil {
		t.Fatalf("host copy missing: %v", err)
	}

	// While mounted, the host copy is the container's content: removing it there
	// removes it from the running service too.
	if err := os.Remove(hostMarker); err != nil {
		t.Fatal(err)
	}
	if _, err := m.apiClient.ExecInService(ctx, name, service, "cat "+marker); err == nil {
		t.Error("deleting a mounted file left it readable in the container")
	}

	if err := m.UnmountPath(name, service, "./conf.d", "/etc/nginx/conf.d"); err != nil {
		t.Fatalf("UnmountPath: %v", err)
	}

	// Unmounted, the service is back on its image's own content.
	out, err := m.apiClient.ExecInService(ctx, name, service, "cat /etc/nginx/conf.d/default.conf")
	if err != nil {
		t.Fatalf("service did not return to its image content: %v", err)
	}
	if !strings.Contains(out, "server") {
		t.Errorf("image config not restored, got %q", out)
	}

	// The host copy survives an unmount; it is the only place that content lives.
	if _, err := os.Stat(filepath.Join(dir, "conf.d")); err != nil {
		t.Errorf("unmount removed the host copy: %v", err)
	}

	// And now the host copy is detached: deleting it cannot touch the container.
	if err := os.RemoveAll(filepath.Join(dir, "conf.d")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.apiClient.ExecInService(ctx, name, service, "cat /etc/nginx/conf.d/default.conf"); err != nil {
		t.Errorf("deleting an unmounted host copy reached into the container: %v", err)
	}

	updated, _, err := m.discovery.GetComposeFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(updated, "./conf.d:/etc/nginx/conf.d") {
		t.Errorf("compose still lists the mount:\n%s", updated)
	}
}
