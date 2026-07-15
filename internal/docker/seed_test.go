package docker

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// tarOf builds an archive laid out the way Docker lays out a copied container
// path. The shapes mirror what `docker cp` really produced from nginx:alpine:
// copying the file /etc/nginx/nginx.conf gave the single entry "nginx.conf",
// and copying the directory /etc/nginx/conf.d gave "conf.d/" followed by
// "conf.d/default.conf".
func tarOf(t *testing.T, entries []tar.Header, bodies map[string]string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i := range entries {
		h := entries[i]
		if body, ok := bodies[h.Name]; ok {
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if body, ok := bodies[h.Name]; ok {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func TestExtractSeedTarFile(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "nginx.conf")
	archive := tarOf(t,
		[]tar.Header{{Name: "nginx.conf", Typeflag: tar.TypeReg, Mode: 0644}},
		map[string]string{"nginx.conf": "worker_processes auto;\n"},
	)

	if err := extractSeedTar(archive, dest, false); err != nil {
		t.Fatal(err)
	}

	// The copied file lands at the destination itself, not inside a directory
	// named after it, which is the whole point of seeding a single-file mount.
	content, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "worker_processes auto;\n" {
		t.Errorf("content = %q", content)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() {
		t.Error("seeded a directory where a file belongs")
	}
	if got := info.Mode().Perm(); got != 0644 {
		t.Errorf("mode = %o, want 644", got)
	}
}

func TestExtractSeedTarDirectory(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "conf.d")
	archive := tarOf(t,
		[]tar.Header{
			{Name: "conf.d/", Typeflag: tar.TypeDir, Mode: 0755},
			{Name: "conf.d/default.conf", Typeflag: tar.TypeReg, Mode: 0644},
			{Name: "conf.d/nested/deep.conf", Typeflag: tar.TypeReg, Mode: 0600},
		},
		map[string]string{
			"conf.d/default.conf":     "server {}\n",
			"conf.d/nested/deep.conf": "deep\n",
		},
	)

	if err := extractSeedTar(archive, dest, true); err != nil {
		t.Fatal(err)
	}

	// The archive's root component is the copied directory itself, so it must
	// not reappear as conf.d/conf.d on the host.
	if _, err := os.Stat(filepath.Join(dest, "conf.d")); !os.IsNotExist(err) {
		t.Error("archive root component was not stripped")
	}

	content, err := os.ReadFile(filepath.Join(dest, "default.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "server {}\n" {
		t.Errorf("content = %q", content)
	}

	if _, err := os.Stat(filepath.Join(dest, "nested", "deep.conf")); err != nil {
		t.Errorf("nested entry missing: %v", err)
	}
}

func TestExtractSeedTarRejectsTraversal(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "conf.d")
	outside := filepath.Join(base, "escaped.conf")

	// Stripping the root leaves "../escaped.conf", which resolves to escaped.conf
	// beside the destination: image content is untrusted, so it must not write
	// there.
	archive := tarOf(t,
		[]tar.Header{{Name: "conf.d/../escaped.conf", Typeflag: tar.TypeReg, Mode: 0644}},
		map[string]string{"conf.d/../escaped.conf": "pwned"},
	)

	if err := extractSeedTar(archive, dest, true); err == nil {
		t.Error("expected an error for an entry escaping the destination")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Error("an archive entry wrote outside the destination")
	}
}

func TestIsSeedable(t *testing.T) {
	base := t.TempDir()

	t.Run("missing path", func(t *testing.T) {
		got, err := isSeedable(filepath.Join(base, "absent"))
		if err != nil || !got {
			t.Errorf("got %v (err %v), want true", got, err)
		}
	})

	t.Run("empty directory", func(t *testing.T) {
		// Deployment creation pre-creates mount directories, so an empty one is
		// the normal state at seeding time.
		dir := filepath.Join(base, "empty")
		if err := os.MkdirAll(dir, 0777); err != nil {
			t.Fatal(err)
		}
		got, err := isSeedable(dir)
		if err != nil || !got {
			t.Errorf("got %v (err %v), want true", got, err)
		}
	})

	t.Run("directory with content", func(t *testing.T) {
		dir := filepath.Join(base, "full")
		if err := os.MkdirAll(dir, 0777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "user.conf"), []byte("mine"), 0644); err != nil {
			t.Fatal(err)
		}
		got, err := isSeedable(dir)
		if err != nil || got {
			t.Errorf("got %v (err %v), want false: user data must never be overwritten", got, err)
		}
	})

	t.Run("existing file", func(t *testing.T) {
		path := filepath.Join(base, "existing.conf")
		if err := os.WriteFile(path, []byte("mine"), 0644); err != nil {
			t.Fatal(err)
		}
		got, err := isSeedable(path)
		if err != nil || got {
			t.Errorf("got %v (err %v), want false", got, err)
		}
	})
}
