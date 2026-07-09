package backup

import (
	"context"
	"io"
	"time"
)

// ObjectInfo describes a stored backup archive in a remote destination.
type ObjectInfo struct {
	Key     string
	Size    int64
	ModTime time.Time
}

// Store is a remote destination that backup archives are mirrored to. The local
// filesystem remains the primary copy and is handled directly by the Manager;
// a Store is only ever a secondary, remote target.
type Store interface {
	// Name is the destination's configured name, surfaced as a backup location.
	Name() string
	// Put writes an object of the given size at key.
	Put(ctx context.Context, key string, r io.Reader, size int64) error
	// Open returns a reader for the object at key.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// List returns objects whose key begins with prefix.
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)
	// Delete removes the object at key. Missing objects are not an error.
	Delete(ctx context.Context, key string) error
	// Stat returns metadata for a single object.
	Stat(ctx context.Context, key string) (ObjectInfo, error)
}
