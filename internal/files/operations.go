package files

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	maxArchiveEntries = 50000
	maxExtractedBytes = 10 << 30
)

type ArchiveEntry struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	IsDir   bool      `json:"is_dir"`
	ModTime time.Time `json:"mod_time"`
}

func copyPath(source, destination string) error {
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("copying symbolic links is not supported")
	}

	rel, err := filepath.Rel(source, destination)
	if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("cannot copy a directory into itself")
	}

	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	if sourceInfo.IsDir() {
		if err := copyDirectory(source, destination, sourceInfo.Mode()); err != nil {
			_ = os.RemoveAll(destination)
			return err
		}
		return nil
	}
	return copyRegularFile(source, destination, sourceInfo.Mode())
}

func copyDirectory(source, destination string, mode os.FileMode) error {
	if err := os.Mkdir(destination, mode.Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		from := filepath.Join(source, entry.Name())
		to := filepath.Join(destination, entry.Name())
		info, err := os.Lstat(from)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("copying symbolic links is not supported")
		}
		if info.IsDir() {
			if err := copyDirectory(from, to, info.Mode()); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("copying %s is not supported", info.Mode().Type())
		}
		if err := copyRegularFile(from, to, info.Mode()); err != nil {
			return err
		}
	}
	return nil
}

func copyRegularFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()

	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		_ = os.Remove(destination)
		return err
	}
	return output.Close()
}

func movePath(source, destination string) error {
	if source == destination {
		return fmt.Errorf("source and destination are the same")
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	if err := os.Rename(source, destination); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if err := copyPath(source, destination); err != nil {
		return err
	}
	return os.RemoveAll(source)
}

func listArchive(path string) ([]ArchiveEntry, error) {
	switch archiveFormat(path) {
	case "zip":
		return listZip(path)
	case "tar", "tar.gz":
		return listTar(path)
	default:
		return nil, fmt.Errorf("unsupported archive format")
	}
}

func extractArchive(source, destination string) error {
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(filepath.Dir(destination), ".flatrun-extract-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)

	switch archiveFormat(source) {
	case "zip":
		err = extractZip(source, temporary)
	case "tar", "tar.gz":
		err = extractTar(source, temporary)
	default:
		err = fmt.Errorf("unsupported archive format")
	}
	if err != nil {
		return err
	}
	return os.Rename(temporary, destination)
}

func pushArchive(source, destination string, deleteMissing bool) (int, error) {
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return 0, err
	}
	staging, err := os.MkdirTemp(filepath.Dir(destination), ".flatrun-push-*")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(staging)

	switch archiveFormat(source) {
	case "zip":
		err = extractZip(source, staging)
	case "tar", "tar.gz":
		err = extractTar(source, staging)
	default:
		err = fmt.Errorf("unsupported archive format")
	}
	if err != nil {
		return 0, err
	}

	files, err := countRegularFiles(staging)
	if err != nil {
		return 0, err
	}
	if !deleteMissing {
		if err := os.MkdirAll(destination, 0755); err != nil {
			return 0, err
		}
		if err := mergeDirectory(staging, destination); err != nil {
			return 0, err
		}
		return files, nil
	}

	if _, err := os.Lstat(destination); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(staging, destination); err != nil {
			return 0, err
		}
		return files, nil
	} else if err != nil {
		return 0, err
	}

	backup, err := os.MkdirTemp(filepath.Dir(destination), ".flatrun-replaced-*")
	if err != nil {
		return 0, err
	}
	if err := os.Remove(backup); err != nil {
		return 0, err
	}
	if err := os.Rename(destination, backup); err != nil {
		return 0, err
	}
	if err := os.Rename(staging, destination); err != nil {
		_ = os.Rename(backup, destination)
		return 0, err
	}
	if err := os.RemoveAll(backup); err != nil {
		return 0, fmt.Errorf("content replaced but old content could not be removed: %w", err)
	}
	return files, nil
}

func countRegularFiles(root string) (int, error) {
	count := 0
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			count++
		}
		return nil
	})
	return count, err
}

func mergeDirectory(source, destination string) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		from := filepath.Join(source, entry.Name())
		to := filepath.Join(destination, entry.Name())
		info, err := os.Lstat(from)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archives containing symbolic links are not supported")
		}
		if info.IsDir() {
			if existing, err := os.Lstat(to); err == nil && !existing.IsDir() {
				return fmt.Errorf("cannot replace file with directory: %s", entry.Name())
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.MkdirAll(to, info.Mode().Perm()); err != nil {
				return err
			}
			if err := mergeDirectory(from, to); err != nil {
				return err
			}
			continue
		}
		if existing, err := os.Lstat(to); err == nil && existing.IsDir() {
			return fmt.Errorf("cannot replace directory with file: %s", entry.Name())
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		input, err := os.Open(from)
		if err != nil {
			return err
		}
		temporary, err := os.CreateTemp(destination, ".flatrun-file-*")
		if err != nil {
			input.Close()
			return err
		}
		temporaryPath := temporary.Name()
		if _, err := io.Copy(temporary, input); err != nil {
			input.Close()
			temporary.Close()
			os.Remove(temporaryPath)
			return err
		}
		input.Close()
		if err := temporary.Chmod(info.Mode().Perm()); err != nil {
			temporary.Close()
			os.Remove(temporaryPath)
			return err
		}
		if err := temporary.Close(); err != nil {
			os.Remove(temporaryPath)
			return err
		}
		if err := os.Rename(temporaryPath, to); err != nil {
			os.Remove(temporaryPath)
			return err
		}
	}
	return nil
}

func archiveFormat(path string) string {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return "tar.gz"
	case strings.HasSuffix(lower, ".tar"):
		return "tar"
	case strings.HasSuffix(lower, ".zip"):
		return "zip"
	default:
		return ""
	}
}

func listZip(path string) ([]ArchiveEntry, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	if len(reader.File) > maxArchiveEntries {
		return nil, fmt.Errorf("archive contains too many entries")
	}
	entries := make([]ArchiveEntry, 0, len(reader.File))
	for _, file := range reader.File {
		entries = append(entries, ArchiveEntry{
			Name:    file.Name,
			Size:    int64(file.UncompressedSize64),
			IsDir:   file.FileInfo().IsDir(),
			ModTime: file.Modified,
		})
	}
	return entries, nil
}

func listTar(path string) ([]ArchiveEntry, error) {
	reader, closeReader, err := openTar(path)
	if err != nil {
		return nil, err
	}
	defer closeReader()

	entries := make([]ArchiveEntry, 0)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(entries) == maxArchiveEntries {
			return nil, fmt.Errorf("archive contains too many entries")
		}
		entries = append(entries, ArchiveEntry{
			Name:    header.Name,
			Size:    header.Size,
			IsDir:   header.FileInfo().IsDir(),
			ModTime: header.ModTime,
		})
	}
	return entries, nil
}

func extractZip(path, destination string) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer reader.Close()
	if len(reader.File) > maxArchiveEntries {
		return fmt.Errorf("archive contains too many entries")
	}

	var extracted int64
	for _, file := range reader.File {
		if file.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive contains a symbolic link")
		}
		extracted += int64(file.UncompressedSize64)
		if extracted > maxExtractedBytes {
			return fmt.Errorf("archive expands beyond the extraction limit")
		}
		target, err := archiveTarget(destination, file.Name)
		if err != nil {
			return err
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, file.Mode().Perm()); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		input, err := file.Open()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, file.Mode().Perm())
		if err != nil {
			input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func extractTar(path, destination string) error {
	reader, closeReader, err := openTar(path)
	if err != nil {
		return err
	}
	defer closeReader()

	var entries, extracted int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		entries++
		if entries > maxArchiveEntries {
			return fmt.Errorf("archive contains too many entries")
		}
		extracted += header.Size
		if extracted > maxExtractedBytes {
			return fmt.Errorf("archive expands beyond the extraction limit")
		}
		target, err := archiveTarget(destination, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, header.FileInfo().Mode().Perm()); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, header.FileInfo().Mode().Perm())
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(output, reader)
			closeErr := output.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("archive contains an unsupported entry type")
		}
	}
	return nil
}

func openTar(path string) (*tar.Reader, func(), error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	if archiveFormat(path) != "tar.gz" {
		return tar.NewReader(file), func() { _ = file.Close() }, nil
	}
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	return tar.NewReader(gzipReader), func() {
		_ = gzipReader.Close()
		_ = file.Close()
	}, nil
}

func archiveTarget(destination, name string) (string, error) {
	normalized := strings.ReplaceAll(name, "\\", "/")
	clean := filepath.Clean(normalized)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry escapes the destination")
	}
	target := filepath.Join(destination, clean)
	rel, err := filepath.Rel(destination, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry escapes the destination")
	}
	return target, nil
}
