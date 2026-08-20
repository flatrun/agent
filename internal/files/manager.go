package files

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type FileInfo struct {
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	Size        int64     `json:"size"`
	IsDir       bool      `json:"is_dir"`
	ModTime     time.Time `json:"mod_time"`
	Permissions string    `json:"permissions"`
	ChildCount  int       `json:"child_count,omitempty"`
}

type Manager struct {
	deploymentsPath string
}

func NewManager(deploymentsPath string) *Manager {
	return &Manager{
		deploymentsPath: deploymentsPath,
	}
}

func (m *Manager) getDeploymentPath(deploymentName string) string {
	return filepath.Join(m.deploymentsPath, deploymentName)
}

func (m *Manager) resolvePath(deploymentName, relativePath string) (string, error) {
	basePath := m.getDeploymentPath(deploymentName)

	cleanPath := filepath.Clean(relativePath)
	if cleanPath == "." {
		cleanPath = ""
	}

	fullPath := filepath.Join(basePath, cleanPath)

	absBase, err := filepath.Abs(basePath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve base path: %w", err)
	}

	absFull, err := filepath.Abs(fullPath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve full path: %w", err)
	}

	baseWithSeparator := absBase
	if !strings.HasSuffix(baseWithSeparator, string(os.PathSeparator)) {
		baseWithSeparator += string(os.PathSeparator)
	}
	if absFull != absBase && !strings.HasPrefix(absFull, baseWithSeparator) {
		return "", fmt.Errorf("path traversal detected")
	}

	return fullPath, nil
}

func (m *Manager) ListFiles(deploymentName, relativePath string) ([]FileInfo, error) {
	dirPath, err := m.resolvePath(deploymentName, relativePath)
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

	basePath := m.getDeploymentPath(deploymentName)
	files := make([]FileInfo, 0, len(entries))

	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		fullPath := filepath.Join(dirPath, entry.Name())
		relPath, _ := filepath.Rel(basePath, fullPath)

		fileInfo := FileInfo{
			Name:        entry.Name(),
			Path:        "/" + relPath,
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

func (m *Manager) CreateDirectory(deploymentName, relativePath string) error {
	dirPath, err := m.resolvePath(deploymentName, relativePath)
	if err != nil {
		return err
	}

	return os.MkdirAll(dirPath, 0755)
}

func (m *Manager) CreateFile(deploymentName, relativePath string) error {
	filePath, err := m.resolvePath(deploymentName, relativePath)
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

func (m *Manager) Chmod(deploymentName, relativePath string, mode os.FileMode) error {
	if mode&^0o777 != 0 {
		return fmt.Errorf("mode must be 0-0777 (no setuid, setgid, or sticky bit)")
	}
	target, err := m.resolvePath(deploymentName, relativePath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(target); err != nil {
		return err
	}
	return os.Chmod(target, mode)
}

func (m *Manager) WriteFile(deploymentName, relativePath string, content io.Reader) error {
	filePath, err := m.resolvePath(deploymentName, relativePath)
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

func (m *Manager) ReadFile(deploymentName, relativePath string) (io.ReadCloser, *FileInfo, error) {
	filePath, err := m.resolvePath(deploymentName, relativePath)
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

	fileInfo := &FileInfo{
		Name:        info.Name(),
		Path:        relativePath,
		Size:        info.Size(),
		IsDir:       false,
		ModTime:     info.ModTime(),
		Permissions: info.Mode().String(),
	}

	return file, fileInfo, nil
}

func (m *Manager) DeleteFile(deploymentName, relativePath string) error {
	filePath, err := m.resolvePath(deploymentName, relativePath)
	if err != nil {
		return err
	}

	basePath := m.getDeploymentPath(deploymentName)
	if filePath == basePath {
		return fmt.Errorf("cannot delete deployment root directory")
	}
	if filePath == activeComposePath(basePath) {
		return fmt.Errorf("cannot delete the active deployment compose file")
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

func (m *Manager) Rename(deploymentName, oldPath, newPath string) error {
	oldFilePath, err := m.resolvePath(deploymentName, oldPath)
	if err != nil {
		return err
	}

	newFilePath, err := m.resolvePath(deploymentName, newPath)
	if err != nil {
		return err
	}
	basePath := m.getDeploymentPath(deploymentName)
	if oldFilePath == activeComposePath(basePath) && !isRootComposePath(basePath, newFilePath) {
		return fmt.Errorf("cannot move the active deployment compose file out of the deployment root")
	}

	return movePath(oldFilePath, newFilePath)
}

func activeComposePath(deploymentPath string) string {
	for _, name := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
		path := filepath.Join(deploymentPath, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	for _, pattern := range []string{"*compose*.yml", "*compose*.yaml"} {
		matches, err := filepath.Glob(filepath.Join(deploymentPath, pattern))
		if err == nil && len(matches) > 0 {
			return matches[0]
		}
	}
	return ""
}

func isRootComposePath(deploymentPath, path string) bool {
	if filepath.Dir(path) != deploymentPath {
		return false
	}
	name := filepath.Base(path)
	if name == "docker-compose.yml" || name == "docker-compose.yaml" || name == "compose.yml" || name == "compose.yaml" {
		return true
	}
	extension := filepath.Ext(name)
	return strings.Contains(name, "compose") && (extension == ".yml" || extension == ".yaml")
}

func (m *Manager) Copy(deploymentName, sourcePath, destinationPath string) error {
	source, err := m.resolvePath(deploymentName, sourcePath)
	if err != nil {
		return err
	}
	destination, err := m.resolvePath(deploymentName, destinationPath)
	if err != nil {
		return err
	}
	return copyPath(source, destination)
}

func (m *Manager) ListArchive(deploymentName, relativePath string) ([]ArchiveEntry, error) {
	archivePath, err := m.resolvePath(deploymentName, relativePath)
	if err != nil {
		return nil, err
	}
	return listArchive(archivePath)
}

func (m *Manager) ExtractArchive(deploymentName, sourcePath, destinationPath string) error {
	source, err := m.resolvePath(deploymentName, sourcePath)
	if err != nil {
		return err
	}
	destination, err := m.resolvePath(deploymentName, destinationPath)
	if err != nil {
		return err
	}
	return extractArchive(source, destination)
}

func (m *Manager) PushArchive(deploymentName, archivePath, destinationPath string, deleteMissing bool) (int, error) {
	destination, err := m.resolvePath(deploymentName, destinationPath)
	if err != nil {
		return 0, err
	}
	root, err := m.resolvePath(deploymentName, "/")
	if err != nil {
		return 0, err
	}
	if deleteMissing && destination == root {
		return 0, fmt.Errorf("delete sync cannot replace the deployment root")
	}
	return pushArchive(archivePath, destination, deleteMissing)
}

func (m *Manager) GetFileInfo(deploymentName, relativePath string) (*FileInfo, error) {
	filePath, err := m.resolvePath(deploymentName, relativePath)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat path: %w", err)
	}

	basePath := m.getDeploymentPath(deploymentName)
	relPath, _ := filepath.Rel(basePath, filePath)

	fileInfo := &FileInfo{
		Name:        info.Name(),
		Path:        "/" + relPath,
		Size:        info.Size(),
		IsDir:       info.IsDir(),
		ModTime:     info.ModTime(),
		Permissions: info.Mode().String(),
	}

	if info.IsDir() {
		entries, _ := os.ReadDir(filePath)
		fileInfo.ChildCount = len(entries)
	}

	return fileInfo, nil
}

func (m *Manager) GetMountPath(deploymentName, relativePath string) (string, error) {
	filePath, err := m.resolvePath(deploymentName, relativePath)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(filePath); err != nil {
		return "", fmt.Errorf("path does not exist: %w", err)
	}

	return filePath, nil
}

func (m *Manager) GetDiskUsage(deploymentName string) (int64, error) {
	basePath := m.getDeploymentPath(deploymentName)

	var totalSize int64
	err := filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			totalSize += info.Size()
		}
		return nil
	})

	return totalSize, err
}
