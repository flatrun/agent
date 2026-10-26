package nginx

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/flatrun/agent/pkg/models"
)

type fileSnapshot struct {
	path    string
	content []byte
	existed bool
}

func takeSnapshot(paths ...string) ([]fileSnapshot, error) {
	snapshots := make([]fileSnapshot, 0, len(paths))
	for _, path := range paths {
		content, err := os.ReadFile(path)
		switch {
		case err == nil:
			snapshots = append(snapshots, fileSnapshot{path: path, content: content, existed: true})
		case os.IsNotExist(err):
			snapshots = append(snapshots, fileSnapshot{path: path})
		default:
			return nil, fmt.Errorf("failed to back up %s: %w", filepath.Base(path), err)
		}
	}
	return snapshots, nil
}

func restoreSnapshot(snapshots []fileSnapshot) error {
	var errs []error
	for _, s := range snapshots {
		if s.existed {
			if err := os.WriteFile(s.path, s.content, 0644); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// applyChange runs write, then validates the whole nginx configuration and
// reloads. When validation or reload fails, the vhost and the shared rate
// limit file are put back as they were before write ran, and nginx is
// reloaded on that restored config, so a rejected change never stays in
// conf.d where it would break the next reload for every deployment.
func (m *Manager) applyChange(name string, write func() error) error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()

	m.mu.RLock()
	paths := []string{
		filepath.Join(m.configPath, name+".conf"),
		filepath.Join(m.configPath, "rate_limits.conf"),
	}
	m.mu.RUnlock()

	snapshots, err := takeSnapshot(paths...)
	if err != nil {
		return err
	}

	if err := write(); err != nil {
		if restoreErr := restoreSnapshot(snapshots); restoreErr != nil {
			log.Printf("warning: failed to restore nginx config for %s: %v", name, restoreErr)
		}
		return err
	}

	if err := m.TestConfig(); err != nil {
		if restoreErr := restoreSnapshot(snapshots); restoreErr != nil {
			return fmt.Errorf("%w (restoring the previous config also failed: %v)", err, restoreErr)
		}
		return err
	}

	if err := m.Reload(); err != nil {
		if restoreErr := restoreSnapshot(snapshots); restoreErr != nil {
			return fmt.Errorf("%w (restoring the previous config also failed: %v)", err, restoreErr)
		}
		if reloadErr := m.Reload(); reloadErr != nil {
			log.Printf("warning: failed to reload nginx on the restored config for %s: %v", name, reloadErr)
		}
		return err
	}

	return nil
}

// ApplyDeployment renders and installs the deployment's vhost and rate
// limits, keeping the previous files when nginx rejects the result.
func (m *Manager) ApplyDeployment(deployment *models.Deployment) error {
	return m.applyChange(deployment.Name, func() error {
		return m.CreateVirtualHost(deployment)
	})
}

// ApplyVirtualHost installs pre-rendered vhost content, keeping the previous
// file when nginx rejects it.
func (m *Manager) ApplyVirtualHost(name, content string) error {
	return m.applyChange(name, func() error {
		return m.WriteVirtualHost(name, content)
	})
}

// RemoveVirtualHost deletes a vhost and its rate limits, putting them back
// when nginx no longer accepts the configuration without them.
func (m *Manager) RemoveVirtualHost(name string) error {
	if !m.VirtualHostExists(name) {
		return nil
	}
	return m.applyChange(name, func() error {
		return m.DeleteVirtualHost(name)
	})
}
