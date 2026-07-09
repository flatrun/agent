package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
)

const locationLocal = "local"

// SetRemotes replaces the set of remote destinations backups are mirrored to.
// It is called at startup and whenever the backup destination config changes.
func (m *Manager) SetRemotes(remotes []Store) {
	m.remotesMu.Lock()
	m.remotes = remotes
	m.remotesMu.Unlock()
}

func (m *Manager) getRemotes() []Store {
	m.remotesMu.RLock()
	defer m.remotesMu.RUnlock()
	return append([]Store(nil), m.remotes...)
}

func backupKey(deploymentName, backupID string) string {
	return deploymentName + "/" + backupID + ".tar.gz"
}

func parseBackupKey(key string) (deployment, backupID string, ok bool) {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) != 2 || !strings.HasSuffix(parts[1], ".tar.gz") {
		return "", "", false
	}
	return parts[0], strings.TrimSuffix(parts[1], ".tar.gz"), true
}

func deploymentFromID(backupID string) string {
	parts := strings.SplitN(backupID, "_", 2)
	if len(parts) < 2 {
		return ""
	}
	return parts[0]
}

// mirrorToRemotes uploads a freshly written local archive to every configured
// remote, returning the names of the destinations that accepted it. A failed
// upload is logged and skipped so a remote outage never fails the backup, whose
// local copy already succeeded.
func (m *Manager) mirrorToRemotes(ctx context.Context, deploymentName, backupID, archivePath string, size int64) []string {
	remotes := m.getRemotes()
	if len(remotes) == 0 {
		return nil
	}

	key := backupKey(deploymentName, backupID)
	var locations []string
	for _, r := range remotes {
		f, err := os.Open(archivePath)
		if err != nil {
			log.Printf("Backup: mirror to %s failed to open archive: %v", r.Name(), err)
			continue
		}
		err = r.Put(ctx, key, f, size)
		f.Close()
		if err != nil {
			log.Printf("Backup: mirror to %s failed: %v", r.Name(), err)
			continue
		}
		locations = append(locations, r.Name())
		log.Printf("Backup mirrored: %s -> %s", backupID, r.Name())
	}
	return locations
}

// ListBackups returns backups from the local disk merged with those in every
// remote destination, deduped by ID with Locations tagged. A remote that fails
// to list is logged and skipped so the local listing still returns.
func (m *Manager) ListBackups(filter *BackupListFilter) ([]Backup, error) {
	localFilter := *filter
	localFilter.Limit = 0
	localFilter.Offset = 0

	locals, err := m.listLocalBackups(&localFilter)
	if err != nil {
		return nil, err
	}

	byID := make(map[string]*Backup, len(locals))
	for i := range locals {
		b := locals[i]
		byID[b.ID] = &b
	}

	prefix := ""
	if filter.DeploymentName != "" {
		prefix = filter.DeploymentName + "/"
	}
	for _, r := range m.getRemotes() {
		objs, err := r.List(context.Background(), prefix)
		if err != nil {
			log.Printf("Backup: list remote %s failed: %v", r.Name(), err)
			continue
		}
		for _, obj := range objs {
			dep, id, ok := parseBackupKey(obj.Key)
			if !ok {
				continue
			}
			if filter.DeploymentName != "" && dep != filter.DeploymentName {
				continue
			}
			if existing, found := byID[id]; found {
				existing.Locations = append(existing.Locations, r.Name())
				continue
			}
			byID[id] = &Backup{
				ID:             id,
				DeploymentName: dep,
				Status:         BackupStatusCompleted,
				Size:           obj.Size,
				CreatedAt:      obj.ModTime,
				Locations:      []string{r.Name()},
			}
		}
	}

	result := make([]Backup, 0, len(byID))
	for _, b := range byID {
		result = append(result, *b)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})

	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}

	return result, nil
}

// GetBackup resolves a backup by ID, preferring the local copy and falling back
// to the first remote that holds it.
func (m *Manager) GetBackup(backupID string) (*Backup, error) {
	if b, err := m.getLocalBackup(backupID); err == nil {
		return b, nil
	}

	dep := deploymentFromID(backupID)
	if dep == "" {
		return nil, fmt.Errorf("invalid backup ID format")
	}

	key := backupKey(dep, backupID)
	for _, r := range m.getRemotes() {
		info, err := r.Stat(context.Background(), key)
		if err != nil {
			if !errors.Is(err, ErrObjectNotFound) {
				log.Printf("Backup: stat remote %s failed: %v", r.Name(), err)
			}
			continue
		}
		return &Backup{
			ID:             backupID,
			DeploymentName: dep,
			Status:         BackupStatusCompleted,
			Size:           info.Size,
			CreatedAt:      info.ModTime,
			Locations:      []string{r.Name()},
		}, nil
	}

	return nil, fmt.Errorf("backup not found: %s", backupID)
}

// DeleteBackup removes a backup from the local disk and every remote it may
// exist in. Remote deletes are idempotent, so a backup already absent remotely
// is not an error.
func (m *Manager) DeleteBackup(backupID string) error {
	dep := deploymentFromID(backupID)
	if dep == "" {
		return fmt.Errorf("invalid backup ID format")
	}

	localExisted := false
	if _, err := m.getLocalBackup(backupID); err == nil {
		if err := m.deleteLocalBackup(backupID); err != nil {
			return err
		}
		localExisted = true
	}

	remotes := m.getRemotes()
	key := backupKey(dep, backupID)
	var firstErr error
	for _, r := range remotes {
		if err := r.Delete(context.Background(), key); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return firstErr
	}

	if !localExisted && len(remotes) == 0 {
		return fmt.Errorf("backup not found: %s", backupID)
	}
	return nil
}

// OpenBackupArchive returns a reader for the backup archive, from local disk if
// present, otherwise streamed from the first remote that holds it.
func (m *Manager) OpenBackupArchive(ctx context.Context, backupID string) (io.ReadCloser, int64, error) {
	if b, err := m.getLocalBackup(backupID); err == nil {
		f, err := os.Open(b.Path)
		if err != nil {
			return nil, 0, err
		}
		return f, b.Size, nil
	}

	dep := deploymentFromID(backupID)
	if dep == "" {
		return nil, 0, fmt.Errorf("invalid backup ID format")
	}

	key := backupKey(dep, backupID)
	for _, r := range m.getRemotes() {
		info, err := r.Stat(ctx, key)
		if err != nil {
			continue
		}
		rc, err := r.Open(ctx, key)
		if err != nil {
			continue
		}
		return rc, info.Size, nil
	}

	return nil, 0, fmt.Errorf("backup not found: %s", backupID)
}

// ensureLocalArchive returns a filesystem path to the backup archive, pulling it
// from a remote into a temp file when the local copy is gone. The returned
// cleanup removes any temp file it created.
func (m *Manager) ensureLocalArchive(ctx context.Context, backup *Backup) (string, func(), error) {
	noop := func() {}
	if backup.Path != "" {
		if _, err := os.Stat(backup.Path); err == nil {
			return backup.Path, noop, nil
		}
	}

	key := backupKey(backup.DeploymentName, backup.ID)
	for _, r := range m.getRemotes() {
		rc, err := r.Open(ctx, key)
		if err != nil {
			if !errors.Is(err, ErrObjectNotFound) {
				log.Printf("Backup: fetch from remote %s failed: %v", r.Name(), err)
			}
			continue
		}
		tmp, err := os.CreateTemp("", "flatrun-remote-*.tar.gz")
		if err != nil {
			rc.Close()
			return "", noop, err
		}
		_, err = io.Copy(tmp, rc)
		rc.Close()
		tmp.Close()
		if err != nil {
			os.Remove(tmp.Name())
			return "", noop, fmt.Errorf("failed to download backup from %s: %w", r.Name(), err)
		}
		return tmp.Name(), func() { os.Remove(tmp.Name()) }, nil
	}

	return "", noop, fmt.Errorf("backup archive not available locally or in any remote: %s", backup.ID)
}
