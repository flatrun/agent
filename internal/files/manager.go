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
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	Size         int64     `json:"size"`
	IsDir        bool      `json:"is_dir"`
	ModTime      time.Time `json:"mod_time"`
	Permissions  string    `json:"permissions"`
	ChildCount   int       `json:"child_count,omitempty"`
}

type Manager struct {
	deploymentsPath string
}

func NewManager(deploymentsPath string) *Manager {
	return &Manager{
		deploymentsPath: deploymentsPath,
	}
}

func (m *Manager) getDeploymentFilesPath(deploymentName string) string {
	return filepath.Join(m.deploymentsPath, deploymentName, "files")
}

func (m *Manager) getDeploymentRootPath(deploymentName string) string {
	return filepath.Join(m.deploymentsPath, deploymentName)
}

func (m *Manager) resolvePath(deploymentName, relativePath string) (string, error) {
	basePath := m.getDeploymentFilesPath(deploymentName)

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

	if !strings.HasPrefix(absFull, absBase) {
		return "", fmt.Errorf("path traversal detected")
	}

	return fullPath, nil
}

func (m *Manager) resolveRootPath(deploymentName, relativePath string) (string, error) {
	basePath := m.getDeploymentRootPath(deploymentName)

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

	if !strings.HasPrefix(absFull, absBase) {
		return "", fmt.Errorf("path traversal detected")
	}

	return fullPath, nil
}

func (m *Manager) EnsureFilesDir(deploymentName string) error {
	filesPath := m.getDeploymentFilesPath(deploymentName)
	return os.MkdirAll(filesPath, 0755)
}

func (m *Manager) ListFiles(deploymentName, relativePath string) ([]FileInfo, error) {
	dirPath, err := m.resolvePath(deploymentName, relativePath)
	if err != nil {
		return nil, err
	}

	if err := m.EnsureFilesDir(deploymentName); err != nil {
		return nil, fmt.Errorf("failed to ensure files directory: %w", err)
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []FileInfo{}, nil
		}
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	basePath := m.getDeploymentFilesPath(deploymentName)
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

func (m *Manager) ListAllFiles(deploymentName, relativePath string) ([]FileInfo, error) {
	dirPath, err := m.resolveRootPath(deploymentName, relativePath)
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

	basePath := m.getDeploymentRootPath(deploymentName)
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

func (m *Manager) ReadAllFile(deploymentName, relativePath string) (io.ReadCloser, *FileInfo, error) {
	filePath, err := m.resolveRootPath(deploymentName, relativePath)
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

func (m *Manager) CreateDirectory(deploymentName, relativePath string) error {
	dirPath, err := m.resolvePath(deploymentName, relativePath)
	if err != nil {
		return err
	}

	return os.MkdirAll(dirPath, 0755)
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

	basePath := m.getDeploymentFilesPath(deploymentName)
	if filePath == basePath {
		return fmt.Errorf("cannot delete root files directory")
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

	return os.Rename(oldFilePath, newFilePath)
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

	basePath := m.getDeploymentFilesPath(deploymentName)
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
	basePath := m.getDeploymentFilesPath(deploymentName)

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
