// Package templatesource delivers the app template catalog to the agent from an
// external origin, so the catalog can change without rebuilding the binary. A
// Source produces the catalog in one uniform shape regardless of where it lives
// (the marketplace API today gated off, a GitHub repo as the working default); a
// Resolver picks the first available Source in priority order; a Syncer writes
// the chosen catalog into the on-disk template cache the deploy and listing paths
// already read. Infra and welcome content stays embedded in the binary and does
// not flow through here.
package templatesource

import (
	"context"
	"fmt"
)

// Template is one catalog entry in the shape the on-disk cache stores: a
// metadata.yml, a docker-compose.yml, and any extra files the template ships,
// keyed by their path relative to the template directory.
type Template struct {
	ID       string
	Version  string
	Metadata []byte
	Compose  []byte
	Files    map[string][]byte
}

// Source produces the app template catalog from one origin. Available reports
// whether the source is configured and worth trying; List returns the full
// catalog. A source that is configured but unreachable should return an error
// from List rather than reporting false from Available, so the Resolver can fall
// through to the next source.
type Source interface {
	Name() string
	Available(ctx context.Context) bool
	List(ctx context.Context) ([]Template, error)
}

// Resolver holds sources in priority order (authoritative first, fallbacks
// after) and returns the catalog from the first one that succeeds.
type Resolver struct {
	sources []Source
}

// NewResolver keeps the sources in the given order; earlier sources win.
func NewResolver(sources ...Source) *Resolver {
	return &Resolver{sources: sources}
}

// Resolve returns the catalog from the first available source that lists
// successfully, along with that source's name. An available source that errors
// is skipped so a broken authoritative source still falls back to the next one;
// the first such error is returned only when every available source failed. When
// no source is available it returns (nil, "", nil) so the caller can keep
// whatever is already cached.
func (r *Resolver) Resolve(ctx context.Context) ([]Template, string, error) {
	var firstErr error
	for _, s := range r.sources {
		if !s.Available(ctx) {
			continue
		}
		templates, err := s.List(ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", s.Name(), err)
			}
			continue
		}
		return templates, s.Name(), nil
	}
	return nil, "", firstErr
}
