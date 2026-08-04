package api

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// seedRepoTemplate copies a template shipped in the repo's templates/ directory
// into a deployment's on-disk template cache, mirroring what the template syncer
// does at runtime. App templates are no longer embedded, so a test exercising one
// must place it in the cache first.
func seedRepoTemplate(t *testing.T, deploymentsPath, id string) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve caller path")
	}
	src := filepath.Join(filepath.Dir(thisFile), "..", "..", "templates", id)
	dest := filepath.Join(deploymentsPath, ".flatrun", "templates", id)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read repo template %q: %v", id, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dest, e.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
