package docker

import (
	"io"
	"os"
	"path/filepath"
)

func copyFile(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	dest, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dest.Close()

	if _, err = io.Copy(dest, source); err != nil {
		return err
	}

	mtime := srcInfo.ModTime()
	return os.Chtimes(dst, mtime, mtime)
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		destPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(destPath, info.Mode()|0700)
		}

		return copyFile(path, destPath)
	})
}
