package templatesource

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Syncer resolves the catalog and writes it into the on-disk template cache the
// deploy and listing paths read from. The cache is the durable store: once
// written it survives restarts and outages, and an operator can populate it by
// hand on an air-gapped host.
type Syncer struct {
	Resolver *Resolver
	CacheDir string
}

// Sync resolves the catalog and materializes it into CacheDir. It returns the
// source used and the number of templates written. When no source is available
// it leaves the cache untouched and returns ("", 0, nil) so a previously synced
// catalog survives an outage. Per-template write failures are skipped rather
// than aborting the whole sync.
func (s *Syncer) Sync(ctx context.Context) (string, int, error) {
	templates, src, err := s.Resolver.Resolve(ctx)
	if err != nil {
		return src, 0, err
	}
	if templates == nil {
		return "", 0, nil
	}
	if err := os.MkdirAll(s.CacheDir, 0o755); err != nil {
		return src, 0, err
	}

	written := 0
	for _, t := range templates {
		if err := s.write(t); err != nil {
			log.Printf("templatesource: skipping template %q: %v", t.ID, err)
			continue
		}
		written++
	}
	return src, written, nil
}

func (s *Syncer) write(t Template) error {
	if t.ID == "" || len(t.Compose) == 0 {
		return fmt.Errorf("template %q: missing id or compose", t.ID)
	}
	if isReservedID(t.ID) {
		return fmt.Errorf("template %q: reserved id", t.ID)
	}
	dir, err := safeJoin(s.CacheDir, t.ID)
	if err != nil {
		return err
	}
	// Replace the directory wholesale so a file dropped upstream between versions
	// does not linger in the cache.
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if len(t.Metadata) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "metadata.yml"), t.Metadata, 0o644); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), t.Compose, 0o644); err != nil {
		return err
	}
	for name, data := range t.Files {
		fpath, err := safeJoin(dir, name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(fpath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(fpath, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// isReservedID rejects catalog ids that collide with the embedded infra and
// welcome content, so an external source cannot overwrite files the agent keeps
// locked to its own version.
func isReservedID(id string) bool {
	return id == "infra" || strings.HasPrefix(id, "infra/") ||
		id == "welcome" || strings.HasPrefix(id, "welcome/")
}

// safeJoin joins rel onto base and guarantees the result stays within base, so a
// hostile template id or file path from a remote source cannot escape the cache
// directory. Cleaning against a root neutralizes any leading "..".
func safeJoin(base, rel string) (string, error) {
	clean := filepath.Clean("/" + strings.ReplaceAll(rel, "\\", "/"))
	joined := filepath.Join(base, clean)
	if joined != base && !strings.HasPrefix(joined, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe path %q", rel)
	}
	return joined, nil
}
