package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestListServicePathAgainstRealContainer lists a real container's directory,
// which is the only way to know the parser agrees with the ls the image ships.
func TestListServicePathAgainstRealContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real container")
	}

	const name = "flatrun-listfiles-integration"
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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

	files, err := m.apiClient.ListServicePath(ctx, name, "app", "/etc/nginx")
	if err != nil {
		t.Fatal(err)
	}

	byName := make(map[string]ContainerFile, len(files))
	for _, f := range files {
		byName[f.Name] = f
	}

	confd, ok := byName["conf.d"]
	if !ok {
		t.Fatalf("conf.d missing from %d entries", len(files))
	}
	if !confd.IsDir {
		t.Error("conf.d should be reported as a directory")
	}
	if confd.Path != "/etc/nginx/conf.d" {
		t.Errorf("path = %q", confd.Path)
	}

	conf, ok := byName["nginx.conf"]
	if !ok {
		t.Fatal("nginx.conf missing")
	}
	if conf.IsDir || conf.Size == 0 {
		t.Errorf("nginx.conf = %+v, want a non-empty file", conf)
	}

	// A path that reaches a shell stays one literal argument.
	if _, err := m.apiClient.ListServicePath(ctx, name, "app", "/etc/nginx; echo pwned"); err == nil {
		t.Error("expected an error for a path that does not exist, not shell execution")
	}
}
