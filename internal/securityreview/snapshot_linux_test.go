package securityreview

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flatrun/agent/pkg/securityprofile"
)

func TestSnapshotBoundsLinksAndRetainsValidFiles(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "valid.php"), []byte("<?php echo 1;"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "oversized"), make([]byte, (1<<20)+1), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, "valid.php"), filepath.Join(root, "hardlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "independent.php"), []byte("<?php echo 2;"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := SnapshotFiles(t.TempDir(), root, []string{"."}, securityprofile.Draft("php").Budget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.ReadFile(filepath.Join(snapshot.Path, "independent.php")); err != nil {
		t.Fatal("valid observation lost", err)
	}
	if len(snapshot.Omissions) == 0 || len(snapshot.Boundaries) < 2 {
		t.Fatalf("missing incomplete coverage: %+v", snapshot)
	}
	for _, path := range []string{"../outside", "/etc/passwd"} {
		if _, err = SnapshotFiles(t.TempDir(), root, []string{path}, securityprofile.Draft("php").Budget); err == nil {
			t.Fatal("escape accepted")
		}
	}
	if _, err = SnapshotFiles(t.TempDir(), filepath.Join(root, "escape"), []string{"."}, securityprofile.Draft("php").Budget); err == nil {
		t.Fatal("symlink root accepted")
	}
}
