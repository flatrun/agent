package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMaterializeMountPreservesRuntimeContent is the case the feature exists
// for: a path the container populated after it started is copied to the host and
// mounted back, and the service carries on with exactly the content it had.
//
// The marker file is written into the running container and exists in no image
// layer, so copying from the image instead of the container would lose it and
// the service would come back on different content.
func TestMaterializeMountPreservesRuntimeContent(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real container")
	}

	const (
		name    = "flatrun-materialize-integration"
		service = "app"
		marker  = "/etc/nginx/conf.d/runtime.conf"
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

	// Content that exists only because the container is running. It is written
	// as a comment because nginx parses every .conf in this directory once the
	// host copy is mounted back.
	if _, err := m.apiClient.ExecInService(ctx, name, service, "echo '# runtime-generated' > "+marker); err != nil {
		t.Fatalf("could not write the runtime marker: %v", err)
	}

	if err := m.MaterializeMount(name, service, "/etc/nginx/conf.d", "./conf.d"); err != nil {
		t.Fatalf("MaterializeMount: %v", err)
	}

	hostMarker := filepath.Join(dir, "conf.d", "runtime.conf")
	content, err := os.ReadFile(hostMarker)
	if err != nil {
		t.Fatalf("runtime content did not reach the host: %v", err)
	}
	if !strings.Contains(string(content), "runtime-generated") {
		t.Errorf("host marker = %q, want it to carry runtime-generated", content)
	}

	// The image's own content has to survive alongside it.
	if _, err := os.Stat(filepath.Join(dir, "conf.d", "default.conf")); err != nil {
		t.Errorf("image content missing from the host copy: %v", err)
	}

	updated, _, err := m.discovery.GetComposeFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(updated, "./conf.d:/etc/nginx/conf.d") {
		t.Errorf("compose file was not given the mount:\n%s", updated)
	}

	deployment, err := m.GetDeployment(name)
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Status != "running" {
		t.Errorf("deployment status = %q, want running after materializing", deployment.Status)
	}

	// The service resumed on the same content, now served from the host.
	out, err := m.apiClient.ExecInService(ctx, name, service, "cat "+marker)
	if err != nil {
		t.Fatalf("service lost the runtime content: %v", err)
	}
	if !strings.Contains(out, "runtime-generated") {
		t.Errorf("container reads %q, want runtime-generated", out)
	}
}
