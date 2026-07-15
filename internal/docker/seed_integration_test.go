package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSeedFromImageAgainstRealImage seeds from a real image, which is the only
// way to know the extractor agrees with what Docker actually streams back.
//
// The single-file case is the bug this feature exists for: mounting a host path
// that does not exist over a container file makes Docker create a directory
// there, and the container then fails to start.
func TestSeedFromImageAgainstRealImage(t *testing.T) {
	if testing.Short() {
		t.Skip("creates a real container")
	}

	api, err := NewAPIClient()
	if err != nil {
		t.Skipf("docker api client unavailable: %v", err)
	}
	defer api.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// nginx:alpine is already used by the compose tests in this package.
	const ref = "nginx:alpine"
	if err := api.EnsureImage(ctx, ref); err != nil {
		t.Skipf("docker unavailable or image cannot be fetched: %v", err)
	}

	t.Run("file", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "nginx.conf")

		if err := api.SeedFromImage(ctx, ref, "/etc/nginx/nginx.conf", dest); err != nil {
			t.Fatal(err)
		}

		info, err := os.Stat(dest)
		if err != nil {
			t.Fatal(err)
		}
		if info.IsDir() {
			t.Fatal("seeded a directory where the image has a file")
		}
		if info.Size() == 0 {
			t.Error("seeded file is empty")
		}
	})

	t.Run("directory", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "conf.d")

		if err := api.SeedFromImage(ctx, ref, "/etc/nginx/conf.d", dest); err != nil {
			t.Fatal(err)
		}

		// Docker roots the archive at the copied directory's own name, so this
		// asserts the root was stripped rather than nested.
		if _, err := os.Stat(filepath.Join(dest, "conf.d")); !os.IsNotExist(err) {
			t.Error("archive root was not stripped")
		}
		if _, err := os.Stat(filepath.Join(dest, "default.conf")); err != nil {
			t.Errorf("expected default.conf from the image: %v", err)
		}
	})

	t.Run("missing container path", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "absent")

		if err := api.SeedFromImage(ctx, ref, "/no/such/path", dest); err == nil {
			t.Error("expected an error for a path the image does not have")
		}
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Error("a failed seed left something behind")
		}
	})
}
