package templatesource

import (
	"context"
	"errors"
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

// versionFile records which source and version the cache holds. The listing
// only reads directories, so the file never shows up as a template.
const versionFile = ".catalog-version"

// Sync resolves the catalog and materializes it into CacheDir. It returns the
// source used and the number of templates written. When the source reports
// that the cache already holds its current version, nothing is downloaded or
// written and it returns (source, 0, nil). When no source is available it
// leaves the cache untouched and returns ("", 0, nil) so a previously synced
// catalog survives an outage. Per-template write failures are skipped rather
// than aborting the whole sync.
func (s *Syncer) Sync(ctx context.Context) (string, int, error) {
	known, version := s.readVersion()
	return s.sync(ctx, known, version)
}

// Refresh downloads and writes the catalog even when it has not changed, for an
// operator who wants the cache rebuilt now.
func (s *Syncer) Refresh(ctx context.Context) (string, int, error) {
	return s.sync(ctx, "", "")
}

func (s *Syncer) sync(ctx context.Context, known, version string) (string, int, error) {
	templates, src, next, err := s.Resolver.ResolveSince(ctx, known, version)
	if errors.Is(err, ErrUnchanged) {
		return src, 0, nil
	}
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
	// A partial write must not be recorded as current, or the failed templates
	// would never be retried while upstream stays the same.
	if written == len(templates) && next != "" {
		s.writeVersion(src, next)
	} else {
		s.writeVersion("", "")
	}
	return src, written, nil
}

func (s *Syncer) readVersion() (string, string) {
	data, err := os.ReadFile(filepath.Join(s.CacheDir, versionFile))
	if err != nil {
		return "", ""
	}
	source, version, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
	return source, version
}

func (s *Syncer) writeVersion(source, version string) {
	path := filepath.Join(s.CacheDir, versionFile)
	if source == "" {
		_ = os.Remove(path)
		return
	}
	if err := os.WriteFile(path, []byte(source+"\n"+version+"\n"), 0o644); err != nil {
		log.Printf("templatesource: record catalog version: %v", err)
	}
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
