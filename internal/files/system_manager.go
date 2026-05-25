package files

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SystemManager provides filesystem access scoped to a configurable root path.
// Unlike Manager, it does not require a deployment name; operations are taken
// against paths relative to the root.
type SystemManager struct {
	root string
}

// NewSystemManager constructs a SystemManager rooted at rootPath. If rootPath
// is empty or invalid it defaults to "/" and a warning is logged: operations
// against the root filesystem are very permissive and should be gated by an
// admin-only permission.
func NewSystemManager(rootPath string) *SystemManager {
	if rootPath == "" {
		rootPath = "/"
	}
	abs, err := filepath.Abs(rootPath)
	if err != nil {
		log.Printf("files.SystemManager: failed to resolve root %q, defaulting to /: %v", rootPath, err)
		abs = "/"
	}
	if abs == "/" {
		log.Printf("files.SystemManager: root is /; operations span the entire filesystem")
	}
	return &SystemManager{root: abs}
}

// Root returns the configured root path.
func (m *SystemManager) Root() string {
	return m.root
}

// resolvePath cleans relativePath, joins it with the root, and ensures the
// result stays within the root prefix. It rejects any path that would escape.
func (m *SystemManager) resolvePath(relativePath string) (string, error) {
	clean := filepath.Clean("/" + strings.TrimPrefix(relativePath, "/"))

	full := filepath.Join(m.root, clean)

	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", fmt.Errorf("failed to resolve path: %w", err)
	}

	if m.root == "/" {
		return absFull, nil
	}

	rootWithSep := m.root
	if !strings.HasSuffix(rootWithSep, string(os.PathSeparator)) {
		rootWithSep += string(os.PathSeparator)
	}
	if absFull != m.root && !strings.HasPrefix(absFull, rootWithSep) {
		return "", fmt.Errorf("path traversal detected")
	}

	return absFull, nil
}

// relPath returns the path of fullPath relative to the root, formatted with a
// leading slash to match how the deployment Manager reports paths.
func (m *SystemManager) relPath(fullPath string) string {
	rel, err := filepath.Rel(m.root, fullPath)
	if err != nil || rel == "." {
		return "/"
	}
	return "/" + filepath.ToSlash(rel)
}

func (m *SystemManager) List(relativePath string) ([]FileInfo, error) {
	dirPath, err := m.resolvePath(relativePath)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []FileInfo{}, nil
		}
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	files := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		fullPath := filepath.Join(dirPath, entry.Name())
		fileInfo := FileInfo{
			Name:        entry.Name(),
			Path:        m.relPath(fullPath),
			Size:        info.Size(),
			IsDir:       entry.IsDir(),
			ModTime:     info.ModTime(),
			Permissions: info.Mode().String(),
		}
		if entry.IsDir() {
			subEntries, _ := os.ReadDir(fullPath)
			fileInfo.ChildCount = len(subEntries)
		}
		files = append(files, fileInfo)
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].IsDir != files[j].IsDir {
			return files[i].IsDir
		}
		return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
	})

	return files, nil
}

func (m *SystemManager) GetFileInfo(relativePath string) (*FileInfo, error) {
	filePath, err := m.resolvePath(relativePath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat path: %w", err)
	}
	fi := &FileInfo{
		Name:        info.Name(),
		Path:        m.relPath(filePath),
		Size:        info.Size(),
		IsDir:       info.IsDir(),
		ModTime:     info.ModTime(),
		Permissions: info.Mode().String(),
	}
	if info.IsDir() {
		entries, _ := os.ReadDir(filePath)
		fi.ChildCount = len(entries)
	}
	return fi, nil
}

func (m *SystemManager) ReadFile(relativePath string) (io.ReadCloser, *FileInfo, error) {
	filePath, err := m.resolvePath(relativePath)
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to stat file: %w", err)
	}
	if info.IsDir() {
		return nil, nil, fmt.Errorf("path is a directory")
	}
	file, err := os.Open(filePath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open file: %w", err)
	}
	fi := &FileInfo{
		Name:        info.Name(),
		Path:        m.relPath(filePath),
		Size:        info.Size(),
		IsDir:       false,
		ModTime:     info.ModTime(),
		Permissions: info.Mode().String(),
	}
	return file, fi, nil
}

func (m *SystemManager) WriteFile(relativePath string, content io.Reader) error {
	filePath, err := m.resolvePath(relativePath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()
	if _, err := io.Copy(file, content); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}
	return nil
}

func (m *SystemManager) CreateDirectory(relativePath string) error {
	dirPath, err := m.resolvePath(relativePath)
	if err != nil {
		return err
	}
	return os.MkdirAll(dirPath, 0755)
}

func (m *SystemManager) CreateFile(relativePath string) error {
	filePath, err := m.resolvePath(relativePath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}
	file, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("file already exists")
		}
		return fmt.Errorf("failed to create file: %w", err)
	}
	return file.Close()
}

func (m *SystemManager) DeleteFile(relativePath string) error {
	filePath, err := m.resolvePath(relativePath)
	if err != nil {
		return err
	}
	if filePath == m.root {
		return fmt.Errorf("cannot delete system files root")
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("failed to stat path: %w", err)
	}
	if info.IsDir() {
		return os.RemoveAll(filePath)
	}
	return os.Remove(filePath)
}

func (m *SystemManager) Chmod(relativePath string, mode os.FileMode) error {
	if mode&^0o777 != 0 {
		return fmt.Errorf("mode must be 0-0777 (no setuid, setgid, or sticky bit)")
	}
	target, err := m.resolvePath(relativePath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(target); err != nil {
		return err
	}
	return os.Chmod(target, mode)
}

// GetDiskUsage returns the total byte size of regular files under relativePath.
// The walk only traverses paths that resolve inside the root. Callers should be
// aware that walking the entire root (e.g. "/") can be very slow.
func (m *SystemManager) GetDiskUsage(relativePath string) (int64, int64, error) {
	dirPath, err := m.resolvePath(relativePath)
	if err != nil {
		return 0, 0, err
	}
	var totalSize, fileCount int64
	err = filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			totalSize += info.Size()
			fileCount++
		}
		return nil
	})
	return totalSize, fileCount, err
}
