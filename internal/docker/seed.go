package docker

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// extractSeedTar writes the tar Docker produces for a copied container path to
// destPath on the host.
//
// Docker roots the archive at the basename of the copied path, so copying
// /etc/nginx/conf.d yields "conf.d/" and "conf.d/default.conf", and copying the
// file /etc/nginx/nginx.conf yields the single entry "nginx.conf". That leading
// component is stripped: destPath is the copied path itself, not its parent.
//
// srcIsDir selects between the two: a file source writes the lone entry to
// destPath, a directory source becomes destPath's contents.
func extractSeedTar(r io.Reader, destPath string, srcIsDir bool) error {
	tr := tar.NewReader(r)

	if !srcIsDir {
		return extractSeedFile(tr, destPath)
	}

	if err := os.MkdirAll(destPath, 0755); err != nil {
		return err
	}

	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		rel := stripRoot(header.Name)
		if rel == "" {
			continue
		}

		target, err := resolveSeedPath(destPath, rel)
		if err != nil {
			return err
		}

		if err := writeSeedEntry(tr, header, target); err != nil {
			return err
		}
	}
}

// extractSeedFile writes the first regular entry of a file copy to destPath.
func extractSeedFile(tr *tar.Reader, destPath string) error {
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("archive held no file to seed %s", destPath)
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return err
		}
		return writeSeedFile(tr, destPath, header.FileInfo().Mode())
	}
}

// writeSeedEntry materialises one archive entry beneath an already-resolved
// target path. Entry types other than files, directories and symlinks (devices,
// fifos) are skipped: an image may carry them, but they are not configuration
// worth reproducing on the host.
func writeSeedEntry(tr *tar.Reader, header *tar.Header, target string) error {
	switch header.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, header.FileInfo().Mode().Perm())
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		return writeSeedFile(tr, target, header.FileInfo().Mode())
	case tar.TypeSymlink:
		// A symlink's target is not followed, so a link pointing outside the
		// deployment only breaks; it cannot be used to write through.
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		_ = os.Remove(target)
		return os.Symlink(header.Linkname, target)
	default:
		return nil
	}
}

func writeSeedFile(r io.Reader, path string, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	return f.Close()
}

// stripRoot removes the archive's leading path component, which Docker sets to
// the basename of the copied container path.
func stripRoot(name string) string {
	name = strings.TrimPrefix(filepath.ToSlash(name), "./")
	if i := strings.Index(name, "/"); i >= 0 {
		return strings.Trim(name[i+1:], "/")
	}
	return ""
}

// resolveSeedPath joins an archive entry onto the destination and refuses
// anything that would land outside it. Image content is untrusted input: an
// entry named ../../etc/passwd would otherwise write through the deployment
// directory.
func resolveSeedPath(destPath, rel string) (string, error) {
	target := filepath.Join(destPath, rel)

	prefix := filepath.Clean(destPath) + string(os.PathSeparator)
	if !strings.HasPrefix(target, prefix) {
		return "", fmt.Errorf("archive entry %q escapes %s", rel, destPath)
	}
	return target, nil
}

// isSeedable reports whether a host path may be seeded. Only a missing path or
// an empty directory qualifies, mirroring the copy-on-first-use semantics of
// named volumes, so content a user already has is never overwritten. Empty
// directories count because deployment creation pre-creates mount directories
// before anything is seeded.
func isSeedable(hostPath string) (bool, error) {
	info, err := os.Stat(hostPath)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, nil
	}

	entries, err := os.ReadDir(hostPath)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}
