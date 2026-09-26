package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flatrun/agent/pkg/version"
)

type Manager struct {
	deploymentsPath string
	backupsPath     string
	jobs            *JobTracker

	remotesMu sync.RWMutex
	remotes   []Store
}

func NewManager(deploymentsPath string) (*Manager, error) {
	backupsPath := filepath.Join(deploymentsPath, ".flatrun", "backups")
	if err := os.MkdirAll(backupsPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create backups directory: %w", err)
	}

	return &Manager{
		deploymentsPath: deploymentsPath,
		backupsPath:     backupsPath,
		jobs:            NewJobTracker(),
	}, nil
}

func (m *Manager) CreateBackup(ctx context.Context, deploymentName string, spec *BackupSpec) (*Backup, error) {
	deploymentPath := filepath.Join(m.deploymentsPath, deploymentName)
	if _, err := os.Stat(deploymentPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("deployment not found: %s", deploymentName)
	}

	backupID := fmt.Sprintf("%s_%s", deploymentName, time.Now().Format("20060102_150405.000000000"))
	backupDir := filepath.Join(m.backupsPath, deploymentName)
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create backup directory: %w", err)
	}

	backup := &Backup{
		ID:             backupID,
		DeploymentName: deploymentName,
		Status:         BackupStatusInProgress,
		CreatedAt:      time.Now(),
		Components:     []string{},
	}
	record := func(kind, name string, required bool, err error) {
		result := ComponentResult{Name: name, Kind: kind, Required: required, Status: ResultStatusCompleted}
		if err != nil {
			result.Status = ResultStatusFailed
			result.Error = err.Error()
		}
		backup.ComponentResults = append(backup.ComponentResults, result)
	}

	tempDir, err := os.MkdirTemp("", "flatrun-backup-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	metadata := BackupMetadata{
		ID:             backupID,
		DeploymentName: deploymentName,
		DeploymentPath: deploymentPath,
		CreatedAt:      time.Now(),
		AgentVersion:   version.Version,
		Components:     BackupComponents{},
	}

	var captureErrors []error
	if spec != nil && len(spec.PreHooks) > 0 {
		err := m.executeHooks(ctx, deploymentName, spec.PreHooks)
		record("preparation", "pre_hooks", true, err)
		if err != nil {
			captureErrors = append(captureErrors, err)
		}
	}

	if err := m.backupComposeFile(deploymentPath, tempDir, &metadata); err != nil {
		record("configuration", "compose", true, err)
		captureErrors = append(captureErrors, err)
	} else {
		record("configuration", "compose", true, nil)
		backup.Components = append(backup.Components, "compose")
	}

	if err := m.backupEnvFile(deploymentPath, tempDir, &metadata); err != nil {
		record("configuration", "environment", true, err)
		captureErrors = append(captureErrors, err)
	} else {
		record("configuration", "environment", true, nil)
		if metadata.Components.EnvFile {
			backup.Components = append(backup.Components, "env")
		}
	}

	if err := m.backupMetadataFile(deploymentPath, tempDir, &metadata); err != nil {
		record("configuration", "metadata", true, err)
		captureErrors = append(captureErrors, err)
	} else {
		record("configuration", "metadata", true, nil)
		if metadata.Components.Metadata {
			backup.Components = append(backup.Components, "metadata")
		}
	}

	var excludes []string
	if spec != nil {
		excludes = spec.ExcludePatterns
	}
	if err := m.backupMountedData(deploymentPath, tempDir, &metadata, excludes); err != nil {
		record("files", "mounted_data", true, err)
		captureErrors = append(captureErrors, err)
	} else {
		record("files", "mounted_data", true, nil)
	}
	if len(metadata.Components.MountedData) > 0 {
		backup.Components = append(backup.Components, "mounted_data")
	}

	if spec != nil && len(spec.ContainerPaths) > 0 {
		results, err := m.backupContainerData(ctx, deploymentName, spec.ContainerPaths, tempDir, &metadata)
		backup.ComponentResults = append(backup.ComponentResults, results...)
		if err != nil {
			captureErrors = append(captureErrors, err)
		}
		if len(metadata.Components.ContainerData) > 0 {
			backup.Components = append(backup.Components, "container_data")
		}
	}

	if spec != nil && len(spec.Databases) > 0 {
		results, err := m.backupDatabases(ctx, deploymentName, spec.Databases, tempDir, &metadata)
		backup.ComponentResults = append(backup.ComponentResults, results...)
		if err != nil {
			captureErrors = append(captureErrors, err)
		}
		if len(metadata.Components.Databases) > 0 {
			backup.Components = append(backup.Components, "databases")
		}
	}

	archivePath := filepath.Join(backupDir, backupID+".tar.gz")
	metadata.ComponentResults = backup.ComponentResults
	if len(captureErrors) == 0 {
		metadataJSON, err := json.MarshalIndent(metadata, "", "  ")
		if err != nil {
			captureErrors = append(captureErrors, err)
		} else if err := os.WriteFile(filepath.Join(tempDir, "backup.json"), metadataJSON, 0600); err != nil {
			captureErrors = append(captureErrors, fmt.Errorf("failed to write backup metadata: %w", err))
		} else if err := m.createArchive(tempDir, archivePath); err != nil {
			captureErrors = append(captureErrors, fmt.Errorf("failed to create backup archive: %w", err))
		} else {
			backup.Path = archivePath
			backup.Locations = []string{locationLocal}
			if info, statErr := os.Stat(archivePath); statErr == nil {
				backup.Size = info.Size()
			}
		}
	}

	var cleanupErr error
	if spec != nil && len(spec.PostHooks) > 0 {
		cleanupErr = m.executeHooks(ctx, deploymentName, spec.PostHooks)
		result := ComponentResult{Name: "post_hooks", Kind: "cleanup", Required: true, Status: ResultStatusCompleted}
		if cleanupErr != nil {
			result.Status = ResultStatusFailed
			result.Error = cleanupErr.Error()
		}
		backup.CleanupResults = append(backup.CleanupResults, result)
	}

	now := time.Now()
	backup.CompletedAt = &now
	if len(captureErrors) > 0 {
		backup.Status = BackupStatusFailed
		backup.Error = errors.Join(captureErrors...).Error()
		_ = m.saveBackupRecord(backup)
		return backup, errors.Join(captureErrors...)
	}

	backup.DestinationResults = m.mirrorToRemotes(ctx, deploymentName, backupID, archivePath, backup.Size)
	succeeded := 0
	for _, result := range backup.DestinationResults {
		if result.Status == ResultStatusCompleted {
			succeeded++
			backup.Locations = append(backup.Locations, result.Name)
		}
	}
	switch {
	case len(backup.DestinationResults) > 0 && succeeded == 0:
		backup.Status = BackupStatusLocalOnly
	case cleanupErr != nil || succeeded < len(backup.DestinationResults):
		backup.Status = BackupStatusPartial
	default:
		backup.Status = BackupStatusCompleted
	}
	if err := m.saveBackupRecord(backup); err != nil {
		return backup, fmt.Errorf("failed to persist backup result: %w", err)
	}

	log.Printf("Backup finished with status %s: %s (%d bytes)", backup.Status, backupID, backup.Size)
	return backup, nil
}

func (m *Manager) backupComposeFile(deploymentPath, tempDir string, metadata *BackupMetadata) error {
	composePath := filepath.Join(deploymentPath, "docker-compose.yml")
	if _, err := os.Stat(composePath); err != nil {
		composePath = filepath.Join(deploymentPath, "compose.yml")
		if _, err := os.Stat(composePath); err != nil {
			return fmt.Errorf("compose file not found")
		}
	}

	destPath := filepath.Join(tempDir, "docker-compose.yml")
	if err := copyFile(composePath, destPath); err != nil {
		return err
	}

	metadata.Components.ComposeFile = true
	return nil
}

func (m *Manager) backupEnvFile(deploymentPath, tempDir string, metadata *BackupMetadata) error {
	envFiles := []string{".env", ".env.flatrun"}
	envDir := filepath.Join(tempDir, "env")
	if err := os.MkdirAll(envDir, 0755); err != nil {
		return fmt.Errorf("failed to create env backup directory: %w", err)
	}

	found := false
	for _, envFile := range envFiles {
		envPath := filepath.Join(deploymentPath, envFile)
		if _, err := os.Stat(envPath); err == nil {
			destPath := filepath.Join(envDir, envFile)
			if err := copyFile(envPath, destPath); err != nil {
				return fmt.Errorf("failed to copy %s: %w", envFile, err)
			}
			found = true
		}
	}

	if found {
		metadata.Components.EnvFile = true
	}
	return nil
}

func (m *Manager) backupMetadataFile(deploymentPath, tempDir string, metadata *BackupMetadata) error {
	metaPath := filepath.Join(deploymentPath, ".flatrun.yml")
	if _, err := os.Stat(metaPath); err != nil {
		return nil
	}

	destPath := filepath.Join(tempDir, ".flatrun.yml")
	if err := copyFile(metaPath, destPath); err != nil {
		return err
	}

	metadata.Components.Metadata = true
	return nil
}

func (m *Manager) backupMountedData(deploymentPath, tempDir string, metadata *BackupMetadata, excludes []string) error {
	dataDir := filepath.Join(tempDir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data backup directory: %w", err)
	}

	commonDataDirs := []string{"data", "uploads", "storage", "config", "logs"}
	for _, dir := range commonDataDirs {
		if matchesExclude(dir, excludes) {
			log.Printf("Backup: skipping %s (excluded, e.g. a database's live files captured by its dump)", dir)
			continue
		}
		srcPath := filepath.Join(deploymentPath, dir)
		if info, err := os.Stat(srcPath); err == nil && info.IsDir() {
			destPath := filepath.Join(dataDir, dir)
			if err := copyDir(srcPath, destPath); err != nil {
				return fmt.Errorf("failed to copy %s: %w", dir, err)
			}
			metadata.Components.MountedData = append(metadata.Components.MountedData, dir)
		}
	}

	return nil
}

// matchesExclude reports whether a mounted-data directory name matches any
// exclude pattern (glob or exact).
func matchesExclude(name string, patterns []string) bool {
	for _, p := range patterns {
		if p == name {
			return true
		}
		if ok, err := filepath.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
}

func (m *Manager) backupContainerData(ctx context.Context, deploymentName string, paths []ContainerPath, tempDir string, metadata *BackupMetadata) ([]ComponentResult, error) {
	containerDir := filepath.Join(tempDir, "container_data")
	if err := os.MkdirAll(containerDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create container data backup directory: %w", err)
	}

	var results []ComponentResult
	var requiredErrors []error
	for _, path := range paths {
		name := fmt.Sprintf("%s:%s", path.Service, path.ContainerPath)
		result := ComponentResult{Name: name, Kind: "files", Required: path.Required, Status: ResultStatusCompleted}
		containerName := fmt.Sprintf("%s-%s", deploymentName, path.Service)
		if path.Service == deploymentName || path.Service == "" {
			containerName = deploymentName
		}

		destPath := filepath.Join(containerDir, path.Service, filepath.Base(path.ContainerPath))
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			result.Status = ResultStatusFailed
			result.Error = fmt.Sprintf("failed to prepare destination: %v", err)
			results = append(results, result)
			if path.Required {
				requiredErrors = append(requiredErrors, fmt.Errorf("failed to create directory for %s: %w", path.ContainerPath, err))
			}
			continue
		}

		cmd := exec.CommandContext(ctx, "docker", "cp", fmt.Sprintf("%s:%s", containerName, path.ContainerPath), destPath)
		if err := cmd.Run(); err != nil {
			result.Status = ResultStatusFailed
			result.Error = "copy failed"
			results = append(results, result)
			if path.Required {
				requiredErrors = append(requiredErrors, fmt.Errorf("failed to copy %s from container %s: %w", path.ContainerPath, containerName, err))
			}
			if !path.Required {
				log.Printf("Backup: optional container path %s not available: %v", path.ContainerPath, err)
			}
			continue
		}

		results = append(results, result)
		metadata.Components.ContainerData = append(metadata.Components.ContainerData, name)
	}

	return results, errors.Join(requiredErrors...)
}

func (m *Manager) backupDatabases(ctx context.Context, deploymentName string, databases []DatabaseSpec, tempDir string, metadata *BackupMetadata) ([]ComponentResult, error) {
	dbDir := filepath.Join(tempDir, "databases")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create databases backup directory: %w", err)
	}

	var results []ComponentResult
	var databaseErrors []error
	for _, db := range databases {
		name := db.Service
		if name == "" {
			name = deploymentName
		}
		result := ComponentResult{Name: name, Kind: "database", Required: true, Status: ResultStatusCompleted}
		var dumpPath string
		var err error

		switch db.Type {
		case "mysql", "mariadb":
			dumpPath, err = m.dumpMySQL(ctx, deploymentName, &db, dbDir)
		case "postgresql", "postgres":
			dumpPath, err = m.dumpPostgres(ctx, deploymentName, &db, dbDir)
		default:
			err = fmt.Errorf("unsupported database type: %s", db.Type)
		}

		if err != nil {
			result.Status = ResultStatusFailed
			result.Error = "dump failed"
			results = append(results, result)
			databaseErrors = append(databaseErrors, fmt.Errorf("failed to dump database %s: %w", name, err))
			continue
		}

		results = append(results, result)
		metadata.Components.Databases = append(metadata.Components.Databases, filepath.Base(dumpPath))
	}

	return results, errors.Join(databaseErrors...)
}

func (m *Manager) backupRecordPath(deploymentName, backupID string) string {
	return filepath.Join(m.backupsPath, deploymentName, backupID+".json")
}

func (m *Manager) saveBackupRecord(backup *Backup) error {
	data, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.backupRecordPath(backup.DeploymentName, backup.ID), data, 0600)
}

func (m *Manager) readBackupRecord(deploymentName, backupID string) (*Backup, error) {
	data, err := os.ReadFile(m.backupRecordPath(deploymentName, backupID))
	if err != nil {
		return nil, err
	}
	var backup Backup
	if err := json.Unmarshal(data, &backup); err != nil {
		return nil, err
	}
	return &backup, nil
}

func (m *Manager) dumpMySQL(ctx context.Context, deploymentName string, db *DatabaseSpec, dbDir string) (string, error) {
	containerName := db.Container
	if containerName == "" {
		containerName = fmt.Sprintf("%s-%s", deploymentName, db.Service)
		if db.Service == deploymentName || db.Service == "" {
			containerName = deploymentName
		}
	}

	label := db.Service
	if label == "" {
		label = deploymentName
	}

	host := db.Host
	if host == "" {
		host = "localhost"
	}
	user := db.User
	if user == "" {
		user = "root"
	}
	database := db.Database
	if database == "" {
		database = deploymentName
	}

	args := []string{"exec"}
	if db.Password != "" {
		args = append(args, "-e", "MYSQL_PWD="+db.Password)
	}
	args = append(args, containerName, "mysqldump", "-h", host, "-u", user,
		"--single-transaction", "--routines", "--triggers")

	dumpFile := filepath.Join(dbDir, fmt.Sprintf("%s_mysql.sql", label))
	if db.AllDatabases {
		args = append(args, "--all-databases")
		dumpFile = filepath.Join(dbDir, fmt.Sprintf("%s_mysql_all.sql", label))
	} else {
		args = append(args, database)
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("mysqldump failed: %w", err)
	}

	if err := os.WriteFile(dumpFile, output, 0644); err != nil {
		return "", fmt.Errorf("failed to write dump file: %w", err)
	}

	return dumpFile, nil
}

func (m *Manager) dumpPostgres(ctx context.Context, deploymentName string, db *DatabaseSpec, dbDir string) (string, error) {
	containerName := db.Container
	if containerName == "" {
		containerName = fmt.Sprintf("%s-%s", deploymentName, db.Service)
		if db.Service == deploymentName || db.Service == "" {
			containerName = deploymentName
		}
	}

	label := db.Service
	if label == "" {
		label = deploymentName
	}

	user := db.User
	if user == "" {
		user = "postgres"
	}
	database := db.Database
	if database == "" {
		database = deploymentName
	}

	args := []string{"exec"}
	if db.Password != "" {
		args = append(args, "-e", fmt.Sprintf("PGPASSWORD=%s", db.Password))
	}

	dumpFile := filepath.Join(dbDir, fmt.Sprintf("%s_postgres.sql", label))
	if db.AllDatabases {
		args = append(args, containerName, "pg_dumpall", "-U", user)
		dumpFile = filepath.Join(dbDir, fmt.Sprintf("%s_postgres_all.sql", label))
	} else {
		args = append(args, containerName, "pg_dump", "-U", user, database)
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("pg_dump failed: %w", err)
	}

	if err := os.WriteFile(dumpFile, output, 0644); err != nil {
		return "", fmt.Errorf("failed to write dump file: %w", err)
	}

	return dumpFile, nil
}

func (m *Manager) executeHooks(ctx context.Context, deploymentName string, hooks []HookSpec) error {
	for _, hook := range hooks {
		containerName := fmt.Sprintf("%s-%s", deploymentName, hook.Service)
		if hook.Service == deploymentName || hook.Service == "" {
			containerName = deploymentName
		}

		timeout := hook.Timeout
		if timeout <= 0 {
			timeout = 60
		}

		hookCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		cmd := exec.CommandContext(hookCtx, "docker", "exec", containerName, "sh", "-c", hook.Command)
		if err := cmd.Run(); err != nil {
			cancel()
			return fmt.Errorf("hook failed for %s: %w", hook.Service, err)
		}
		cancel()
	}
	return nil
}

func (m *Manager) createArchive(sourceDir, destPath string) error {
	file, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer file.Close()

	gzWriter := gzip.NewWriter(file)
	defer gzWriter.Close()

	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	return filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}

		if relPath == "." {
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}

		header.Name = relPath

		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		if !info.IsDir() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()

			_, err = io.Copy(tarWriter, file)
			return err
		}

		return nil
	})
}

func (m *Manager) listLocalBackups(filter *BackupListFilter) ([]Backup, error) {
	var backups []Backup

	deploymentDirs, err := os.ReadDir(m.backupsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return backups, nil
		}
		return nil, err
	}

	for _, deploymentDir := range deploymentDirs {
		if !deploymentDir.IsDir() {
			continue
		}

		if filter.DeploymentName != "" && deploymentDir.Name() != filter.DeploymentName {
			continue
		}

		backupDir := filepath.Join(m.backupsPath, deploymentDir.Name())
		files, err := os.ReadDir(backupDir)
		if err != nil {
			continue
		}

		seen := make(map[string]bool)
		for _, file := range files {
			if !strings.HasSuffix(file.Name(), ".json") {
				continue
			}
			backupID := strings.TrimSuffix(file.Name(), ".json")
			backup, err := m.readBackupRecord(deploymentDir.Name(), backupID)
			if err != nil {
				continue
			}
			if backup.Path != "" {
				if info, statErr := os.Stat(backup.Path); statErr == nil {
					backup.Size = info.Size()
				} else {
					backup.Path = ""
					backup.Locations = removeLocation(backup.Locations, locationLocal)
				}
			}
			if filter.Status == "" || backup.Status == filter.Status {
				backups = append(backups, *backup)
			}
			seen[backupID] = true
		}

		for _, file := range files {
			if !strings.HasSuffix(file.Name(), ".tar.gz") {
				continue
			}

			info, err := file.Info()
			if err != nil {
				continue
			}

			backupID := strings.TrimSuffix(file.Name(), ".tar.gz")
			if seen[backupID] {
				continue
			}
			backup := Backup{
				ID:             backupID,
				DeploymentName: deploymentDir.Name(),
				Status:         BackupStatusCompleted,
				Size:           info.Size(),
				Path:           filepath.Join(backupDir, file.Name()),
				CreatedAt:      info.ModTime(),
				Locations:      []string{locationLocal},
			}

			if filter.Status == "" || backup.Status == filter.Status {
				backups = append(backups, backup)
			}
		}
	}

	sort.Slice(backups, func(i, j int) bool {
		return backups[i].CreatedAt.After(backups[j].CreatedAt)
	})

	if filter.Limit > 0 && len(backups) > filter.Limit {
		backups = backups[:filter.Limit]
	}

	return backups, nil
}

func (m *Manager) getLocalBackup(backupID string) (*Backup, error) {
	parts := strings.SplitN(backupID, "_", 2)
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid backup ID format")
	}

	deploymentName := parts[0]
	backupPath := filepath.Join(m.backupsPath, deploymentName, backupID+".tar.gz")
	if backup, recordErr := m.readBackupRecord(deploymentName, backupID); recordErr == nil {
		if info, statErr := os.Stat(backupPath); statErr == nil {
			backup.Path = backupPath
			backup.Size = info.Size()
			if !containsLocation(backup.Locations, locationLocal) {
				backup.Locations = append([]string{locationLocal}, backup.Locations...)
			}
			return backup, nil
		}
		backup.Path = ""
		backup.Locations = removeLocation(backup.Locations, locationLocal)
		return backup, nil
	}

	info, err := os.Stat(backupPath)
	if err != nil {
		return nil, fmt.Errorf("backup not found: %s", backupID)
	}

	return &Backup{
		ID:             backupID,
		DeploymentName: deploymentName,
		Status:         BackupStatusCompleted,
		Size:           info.Size(),
		Path:           backupPath,
		CreatedAt:      info.ModTime(),
		Locations:      []string{locationLocal},
	}, nil
}

func (m *Manager) deleteLocalBackup(backupID string) error {
	backup, err := m.getLocalBackup(backupID)
	if err != nil {
		return err
	}

	if backup.Path != "" {
		if err := os.Remove(backup.Path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	_ = os.Remove(m.backupRecordPath(backup.DeploymentName, backup.ID))
	return nil
}

func containsLocation(locations []string, target string) bool {
	for _, location := range locations {
		if location == target {
			return true
		}
	}
	return false
}

func removeLocation(locations []string, target string) []string {
	filtered := locations[:0]
	for _, location := range locations {
		if location != target {
			filtered = append(filtered, location)
		}
	}
	return filtered
}

func (m *Manager) GetBackupPath(backupID string) (string, error) {
	backup, err := m.getLocalBackup(backupID)
	if err != nil {
		return "", err
	}
	if backup.Path == "" {
		return "", fmt.Errorf("backup archive is unavailable: %s", backupID)
	}
	return backup.Path, nil
}

// CleanupOldBackups prunes the local on-disk copies beyond keepCount. Retention
// applies to local disk only; remote copies are governed by the destination's
// own lifecycle policy and are never deleted here.
func (m *Manager) CleanupOldBackups(deploymentName string, keepCount int) (int, error) {
	backups, err := m.listLocalBackups(&BackupListFilter{DeploymentName: deploymentName})
	if err != nil {
		return 0, err
	}

	usable := backups[:0]
	for _, backup := range backups {
		if backup.Path != "" && backup.Status != BackupStatusFailed {
			usable = append(usable, backup)
		}
	}
	if len(usable) <= keepCount {
		return 0, nil
	}

	deleted := 0
	for _, backup := range usable[keepCount:] {
		if err := m.deleteLocalBackup(backup.ID); err != nil {
			log.Printf("Failed to delete old backup %s: %v", backup.ID, err)
			continue
		}
		deleted++
	}

	return deleted, nil
}

func (m *Manager) RestoreBackup(ctx context.Context, req *RestoreBackupRequest) error {
	backup, err := m.GetBackup(req.BackupID)
	if err != nil {
		return err
	}

	deploymentName := backup.DeploymentName
	if req.DeploymentName != "" {
		deploymentName = req.DeploymentName
	}

	deploymentPath := filepath.Join(m.deploymentsPath, deploymentName)

	tempDir, err := os.MkdirTemp("", "flatrun-restore-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	archivePath, cleanup, err := m.ensureLocalArchive(ctx, backup)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := m.extractArchive(archivePath, tempDir); err != nil {
		return fmt.Errorf("failed to extract backup: %w", err)
	}

	metadataPath := filepath.Join(tempDir, "backup.json")
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		return fmt.Errorf("failed to read backup metadata: %w", err)
	}

	var metadata BackupMetadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		return fmt.Errorf("failed to parse backup metadata: %w", err)
	}

	if req.StopFirst {
		log.Printf("Restore: stopping deployment %s", deploymentName)
		cmd := exec.CommandContext(ctx, "docker", "compose", "-f",
			filepath.Join(deploymentPath, "docker-compose.yml"), "stop")
		cmd.Dir = deploymentPath
		if err := cmd.Run(); err != nil {
			log.Printf("Restore: warning - failed to stop deployment: %v", err)
		}
	}

	if err := os.MkdirAll(deploymentPath, 0755); err != nil {
		return fmt.Errorf("failed to create deployment directory: %w", err)
	}

	if metadata.Components.ComposeFile {
		if err := m.restoreComposeFile(tempDir, deploymentPath); err != nil {
			return fmt.Errorf("failed to restore compose file: %w", err)
		}
		log.Printf("Restore: restored compose file")
	}

	if metadata.Components.EnvFile {
		if err := m.restoreEnvFiles(tempDir, deploymentPath); err != nil {
			log.Printf("Restore: warning - failed to restore env files: %v", err)
		} else {
			log.Printf("Restore: restored env files")
		}
	}

	if metadata.Components.Metadata {
		if err := m.restoreMetadataFile(tempDir, deploymentPath); err != nil {
			log.Printf("Restore: warning - failed to restore metadata file: %v", err)
		} else {
			log.Printf("Restore: restored metadata file")
		}
	}

	if req.RestoreData && len(metadata.Components.MountedData) > 0 {
		if err := m.restoreMountedData(tempDir, deploymentPath, metadata.Components.MountedData); err != nil {
			log.Printf("Restore: warning - failed to restore mounted data: %v", err)
		} else {
			log.Printf("Restore: restored mounted data")
		}
	}

	if req.StopFirst {
		log.Printf("Restore: starting deployment %s", deploymentName)
		cmd := exec.CommandContext(ctx, "docker", "compose", "-f",
			filepath.Join(deploymentPath, "docker-compose.yml"), "up", "-d")
		cmd.Dir = deploymentPath
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to start deployment after restore: %w", err)
		}

		time.Sleep(5 * time.Second)
	}

	if req.RestoreData && len(metadata.Components.ContainerData) > 0 {
		if err := m.restoreContainerData(ctx, deploymentName, tempDir, metadata.Components.ContainerData); err != nil {
			log.Printf("Restore: warning - failed to restore container data: %v", err)
		} else {
			log.Printf("Restore: restored container data")
		}
	}

	if req.RestoreDB && len(metadata.Components.Databases) > 0 {
		if err := m.restoreDatabases(ctx, deploymentName, tempDir, metadata.Components.Databases); err != nil {
			log.Printf("Restore: warning - failed to restore databases: %v", err)
		} else {
			log.Printf("Restore: restored databases")
		}
	}

	log.Printf("Restore completed for %s from backup %s", deploymentName, req.BackupID)
	return nil
}

func (m *Manager) extractArchive(archivePath, destDir string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()

	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		destPath := filepath.Join(destDir, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destPath, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
				return err
			}
			outFile, err := os.Create(destPath)
			if err != nil {
				return err
			}
			if _, err := io.Copy(outFile, tarReader); err != nil {
				outFile.Close()
				return err
			}
			outFile.Close()
		}
	}

	return nil
}

func (m *Manager) restoreComposeFile(tempDir, deploymentPath string) error {
	srcPath := filepath.Join(tempDir, "docker-compose.yml")
	destPath := filepath.Join(deploymentPath, "docker-compose.yml")
	return copyFile(srcPath, destPath)
}

func (m *Manager) restoreEnvFiles(tempDir, deploymentPath string) error {
	envDir := filepath.Join(tempDir, "env")
	if _, err := os.Stat(envDir); os.IsNotExist(err) {
		return nil
	}

	files, err := os.ReadDir(envDir)
	if err != nil {
		return err
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}
		srcPath := filepath.Join(envDir, file.Name())
		destPath := filepath.Join(deploymentPath, file.Name())
		if err := copyFile(srcPath, destPath); err != nil {
			return err
		}
	}

	return nil
}

func (m *Manager) restoreMetadataFile(tempDir, deploymentPath string) error {
	srcPath := filepath.Join(tempDir, ".flatrun.yml")
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		return nil
	}
	destPath := filepath.Join(deploymentPath, ".flatrun.yml")
	return copyFile(srcPath, destPath)
}

func (m *Manager) restoreMountedData(tempDir, deploymentPath string, dataItems []string) error {
	dataDir := filepath.Join(tempDir, "data")
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		return nil
	}

	for _, item := range dataItems {
		srcPath := filepath.Join(dataDir, item)
		destPath := filepath.Join(deploymentPath, item)
		if _, err := os.Stat(srcPath); os.IsNotExist(err) {
			continue
		}
		if err := os.RemoveAll(destPath); err != nil {
			log.Printf("Restore: warning - failed to remove existing %s: %v", item, err)
		}
		if err := copyDir(srcPath, destPath); err != nil {
			return fmt.Errorf("failed to restore %s: %w", item, err)
		}
	}

	return nil
}

func (m *Manager) restoreContainerData(ctx context.Context, deploymentName, tempDir string, items []string) error {
	containerDir := filepath.Join(tempDir, "container_data")
	if _, err := os.Stat(containerDir); os.IsNotExist(err) {
		return nil
	}

	for _, item := range items {
		parts := strings.SplitN(item, ":", 2)
		if len(parts) != 2 {
			continue
		}
		service := parts[0]
		containerPath := parts[1]

		containerName := fmt.Sprintf("%s-%s", deploymentName, service)
		srcPath := filepath.Join(containerDir, service, filepath.Base(containerPath))

		if _, err := os.Stat(srcPath); os.IsNotExist(err) {
			continue
		}

		cmd := exec.CommandContext(ctx, "docker", "cp", srcPath, fmt.Sprintf("%s:%s", containerName, containerPath))
		if err := cmd.Run(); err != nil {
			log.Printf("Restore: warning - failed to restore %s to container %s: %v", containerPath, containerName, err)
		}
	}

	return nil
}

func (m *Manager) restoreDatabases(ctx context.Context, deploymentName, tempDir string, dbFiles []string) error {
	dbDir := filepath.Join(tempDir, "databases")
	if _, err := os.Stat(dbDir); os.IsNotExist(err) {
		return nil
	}

	for _, dbFile := range dbFiles {
		dumpPath := filepath.Join(dbDir, dbFile)
		if _, err := os.Stat(dumpPath); os.IsNotExist(err) {
			continue
		}

		if strings.Contains(dbFile, "_mysql.sql") {
			service := strings.TrimSuffix(dbFile, "_mysql.sql")
			if err := m.restoreMySQL(ctx, deploymentName, service, dumpPath); err != nil {
				log.Printf("Restore: warning - failed to restore MySQL database %s: %v", service, err)
			}
		} else if strings.Contains(dbFile, "_postgres.sql") {
			service := strings.TrimSuffix(dbFile, "_postgres.sql")
			if err := m.restorePostgres(ctx, deploymentName, service, dumpPath); err != nil {
				log.Printf("Restore: warning - failed to restore PostgreSQL database %s: %v", service, err)
			}
		}
	}

	return nil
}

func (m *Manager) restoreMySQL(ctx context.Context, deploymentName, service, dumpPath string) error {
	containerName := fmt.Sprintf("%s-%s", deploymentName, service)

	dumpContent, err := os.ReadFile(dumpPath)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", containerName, "mysql", "-u", "root", deploymentName)
	cmd.Stdin = strings.NewReader(string(dumpContent))
	return cmd.Run()
}

func (m *Manager) restorePostgres(ctx context.Context, deploymentName, service, dumpPath string) error {
	containerName := fmt.Sprintf("%s-%s", deploymentName, service)

	dumpContent, err := os.ReadFile(dumpPath)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", containerName, "psql", "-U", "postgres", deploymentName)
	cmd.Stdin = strings.NewReader(string(dumpContent))
	return cmd.Run()
}

func copyFile(src, dst string) error {
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

	_, err = io.Copy(dest, source)
	return err
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
			return os.MkdirAll(destPath, info.Mode())
		}

		return copyFile(path, destPath)
	})
}
