package securityreview

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"
)

type Change struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Digest string `json:"digest,omitempty"`
}
type Manifest struct {
	files map[string]string

	APIVersion         string    `json:"api_version"`
	Deployment         string    `json:"deployment"`
	Scope              string    `json:"scope"`
	Sequence           int64     `json:"sequence"`
	ObservedAt         time.Time `json:"observed_at"`
	Changes            []Change  `json:"changes"`
	ManifestProcessing string    `json:"manifest_processing"`
	WindowCoverage     string    `json:"window_coverage"`
}

func (s *Store) ReconcileSnapshot(dep, scope string, snapshot Snapshot) (Manifest, error) {
	current := map[string]string{}
	err := filepath.WalkDir(snapshot.Path, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, err = io.Copy(hash, file)
		file.Close()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(snapshot.Path, path)
		if err != nil {
			return err
		}
		current[rel] = hex.EncodeToString(hash.Sum(nil))
		return nil
	})
	if err != nil {
		return Manifest{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Manifest{}, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("CREATE TABLE IF NOT EXISTS manifests (deployment TEXT, scope TEXT, sequence INTEGER, body BLOB, files BLOB, PRIMARY KEY(deployment,scope,sequence))"); err != nil {
		return Manifest{}, err
	}
	previous := map[string]string{}
	var sequence int64
	var data []byte
	rowErr := tx.QueryRow("SELECT sequence,files FROM manifests WHERE deployment=? AND scope=? ORDER BY sequence DESC LIMIT 1", dep, scope).Scan(&sequence, &data)
	if rowErr != nil && !errors.Is(rowErr, sql.ErrNoRows) {
		return Manifest{}, rowErr
	}
	if rowErr == nil {
		if err = json.Unmarshal(data, &previous); err != nil {
			return Manifest{}, err
		}
	}
	manifest := Manifest{APIVersion: "flatrun.security-manifest/v1", Deployment: dep, Scope: scope, Sequence: sequence + 1, ObservedAt: time.Now().UTC(), Changes: []Change{}, ManifestProcessing: "complete", WindowCoverage: "unknown"}
	for path, digest := range current {
		if previous[path] == digest {
			continue
		}
		kind := "changed"
		if previous[path] == "" {
			kind = "added"
		}
		manifest.Changes = append(manifest.Changes, Change{path, kind, digest})
	}
	if len(snapshot.Omissions) == 0 && len(snapshot.Boundaries) == 0 {
		for path := range previous {
			if _, exists := current[path]; !exists {
				manifest.Changes = append(manifest.Changes, Change{Path: path, Kind: "deleted"})
			}
		}
	} else {
		manifest.ManifestProcessing = "partial"
		for path, digest := range previous {
			if _, exists := current[path]; !exists {
				current[path] = digest
			}
		}
	}
	slices.SortFunc(manifest.Changes, func(a, b Change) int {
		if a.Path < b.Path {
			return -1
		}
		if a.Path > b.Path {
			return 1
		}
		return 0
	})
	manifest.files = current
	return manifest, nil
}
