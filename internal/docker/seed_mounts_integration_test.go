package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSeedMountsFillsOptedInMountsOnly covers the fresh-deploy case, where no
// container exists yet and the image is the only thing to copy from.
func TestSeedMountsFillsOptedInMountsOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("creates a real container")
	}

	const name = "flatrun-seedmounts-integration"
	base := t.TempDir()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	compose := "name: " + name + `
services:
  app:
    image: nginx:alpine
    volumes:
      - ./conf.d:/etc/nginx/conf.d
      - ./untouched:/usr/share/nginx/html
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(base)
	if m.apiClient == nil {
		t.Skip("docker api client unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := m.apiClient.ListLiveComposeContainers(ctx); err != nil {
		t.Skipf("docker daemon unreachable: %v", err)
	}

	// Deployment creation pre-creates mount directories, so seeding meets empty
	// directories rather than absent ones.
	for _, sub := range []string{"conf.d", "untouched"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0777); err != nil {
			t.Fatal(err)
		}
	}

	if err := m.SeedMounts(name, []string{"./conf.d"}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "conf.d", "default.conf")); err != nil {
		t.Errorf("opted-in mount was not seeded from the image: %v", err)
	}

	// A mount nobody asked to seed stays exactly as it was.
	entries, err := os.ReadDir(filepath.Join(dir, "untouched"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a mount that was not opted in got %d entries", len(entries))
	}
}

func TestSeedMountsLeavesExistingContentAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("creates a real container")
	}

	const name = "flatrun-seedmounts-existing"
	base := t.TempDir()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(filepath.Join(dir, "conf.d"), 0777); err != nil {
		t.Fatal(err)
	}

	compose := "name: " + name + `
services:
  app:
    image: nginx:alpine
    volumes:
      - ./conf.d:/etc/nginx/conf.d
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}

	mine := filepath.Join(dir, "conf.d", "mine.conf")
	if err := os.WriteFile(mine, []byte("# mine\n"), 0644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(base)
	if m.apiClient == nil {
		t.Skip("docker api client unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := m.apiClient.ListLiveComposeContainers(ctx); err != nil {
		t.Skipf("docker daemon unreachable: %v", err)
	}

	if err := m.SeedMounts(name, []string{"./conf.d"}); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(mine)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "# mine\n" {
		t.Errorf("existing file = %q, want it untouched", content)
	}
	// The image's own config must not appear beside it: a mount with content is
	// skipped whole, never merged.
	if _, err := os.Stat(filepath.Join(dir, "conf.d", "default.conf")); !os.IsNotExist(err) {
		t.Error("seeded into a mount that already had content")
	}
}
