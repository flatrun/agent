package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const absentMarker = "absent"

type DriftError struct {
	Paths []string
}

func (e *DriftError) Error() string {
	return fmt.Sprintf("state changed since plan was created: %s", strings.Join(e.Paths, ", "))
}

func hashFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return absentMarker
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// SnapshotFiles hashes the given paths. Relative paths are resolved
// against base and stored relative; absolute paths are stored as-is.
// Missing files hash to the literal "absent" so creation is detectable.
func SnapshotFiles(base string, paths ...string) map[string]string {
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		full := p
		if !filepath.IsAbs(p) {
			full = filepath.Join(base, p)
		}
		out[p] = hashFile(full)
	}
	return out
}

// VerifySnapshot re-hashes every snapshotted file and returns a
// *DriftError listing the paths whose content changed since plan time.
func VerifySnapshot(base string, snapshot map[string]string) error {
	var drifted []string
	for p, want := range snapshot {
		full := p
		if !filepath.IsAbs(p) {
			full = filepath.Join(base, p)
		}
		if hashFile(full) != want {
			drifted = append(drifted, p)
		}
	}
	if len(drifted) > 0 {
		sort.Strings(drifted)
		return &DriftError{Paths: drifted}
	}
	return nil
}
