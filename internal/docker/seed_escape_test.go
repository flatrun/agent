package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMaterializeMountRefusesEscapingHostPath pins that a host path is confined
// to the deployment directory. The path arrives from an API request, so without
// this a caller could write a container's content anywhere the agent can reach.
func TestMaterializeMountRefusesEscapingHostPath(t *testing.T) {
	base := t.TempDir()
	name := "escape-test"
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

	for _, hostPath := range []string{
		"../../escaped",
		"./conf/../../../escaped",
		"/etc/flatrun-escaped",
	} {
		t.Run(hostPath, func(t *testing.T) {
			err := m.MaterializeMount(name, "app", "/etc/nginx/conf.d", hostPath)
			if err == nil {
				t.Fatalf("host path %q was accepted, it must stay inside the deployment", hostPath)
			}
			if !strings.Contains(err.Error(), "deployment directory") {
				t.Errorf("error = %v, want it to say the path must stay inside the deployment directory", err)
			}
		})
	}
}

// TestSeedMountsRefusesEscapingHostPath covers the same confinement on the
// seeding path, where the mount is read from the compose file.
func TestSeedMountsRefusesEscapingHostPath(t *testing.T) {
	base := t.TempDir()
	name := "seed-escape-test"
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	compose := "name: " + name + `
services:
  app:
    image: nginx:alpine
    volumes:
      - ../../escaped:/etc/nginx/conf.d
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(base)

	err := m.SeedMounts(name, []string{"../../escaped"})
	if err == nil {
		t.Fatal("a mount pointing outside the deployment was seeded")
	}
	if _, statErr := os.Stat(filepath.Join(base, "..", "escaped")); statErr == nil {
		t.Error("seeding wrote outside the deployment directory")
	}
}
