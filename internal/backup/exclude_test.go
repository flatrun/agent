package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMatchesExclude(t *testing.T) {
	if !matchesExclude("data", []string{"data"}) {
		t.Fatal("exact match failed")
	}
	if !matchesExclude("data", []string{"da*"}) {
		t.Fatal("glob match failed")
	}
	if matchesExclude("uploads", []string{"data"}) {
		t.Fatal("should not match")
	}
}

func TestBackupMountedData_SkipsExcluded(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dep := t.TempDir()
	for _, d := range []string{"data", "uploads"} {
		os.MkdirAll(filepath.Join(dep, d), 0o755)
		os.WriteFile(filepath.Join(dep, d, "f"), []byte("x"), 0o644)
	}
	out := t.TempDir()
	var md BackupMetadata
	if err := m.backupMountedData(dep, out, &md, []string{"data"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "data", "data")); err == nil {
		t.Fatal("excluded 'data' dir should not be copied")
	}
	if _, err := os.Stat(filepath.Join(out, "data", "uploads")); err != nil {
		t.Fatal("'uploads' should be copied")
	}
}
