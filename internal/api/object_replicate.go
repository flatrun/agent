package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/flatrun/agent/internal/backup"
	"github.com/gin-gonic/gin"
)

// s3StoreByName resolves a registered store by name into an S3 client without
// writing a response, for callers that handle their own errors.
func (s *Server) s3StoreByName(name string) (*backup.S3Store, error) {
	dest, ok := s.findDestinationByName(name)
	if !ok {
		return nil, fmt.Errorf("object store %q not found", name)
	}
	store, err := s.buildStore(dest)
	if err != nil {
		return nil, err
	}
	s3store, ok := store.(*backup.S3Store)
	if !ok {
		return nil, fmt.Errorf("store %q is not S3-compatible", name)
	}
	return s3store, nil
}

// copyObject streams a source object through a temp file before uploading it.
// The S3 client must sign the request body, which requires a seekable reader;
// an object read stream is not seekable, so it is buffered to disk first. Disk
// (not memory) keeps large backup archives from exhausting RAM.
func copyObject(ctx context.Context, src, dst *backup.S3Store, key string) (int64, error) {
	reader, err := src.Open(ctx, key)
	if err != nil {
		return 0, err
	}
	defer reader.Close()

	tmp, err := os.CreateTemp("", "flatrun-replicate-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	n, err := io.Copy(tmp, reader)
	if err != nil {
		return 0, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	if err := dst.Put(ctx, key, tmp, n); err != nil {
		return 0, err
	}
	return n, nil
}

// replicateStore copies every object from the store named in the path to a
// target store. It is incremental: an object already present in the target at
// the same size is skipped, so re-running only moves what changed. This backs
// offsite copies (managed to external) and local caches (external to managed).
func (s *Server) replicateStore(c *gin.Context) {
	src, ok := s.storeS3ByName(c)
	if !ok {
		return
	}

	var req struct {
		Target string `json:"target" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Target == c.Param("name") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a store cannot replicate to itself"})
		return
	}

	dst, err := s.s3StoreByName(req.Target)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	if err := dst.EnsureBucket(ctx); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "target bucket unavailable: " + err.Error()})
		return
	}

	objects, err := src.ListObjects(ctx, "")
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not list source: " + err.Error()})
		return
	}

	var copied, skipped, failed int
	var bytes int64
	for _, o := range objects {
		if info, err := dst.Stat(ctx, o.Key); err == nil && info.Size == o.Size {
			skipped++
			continue
		}
		n, err := copyObject(ctx, src, dst, o.Key)
		if err != nil {
			failed++
			continue
		}
		copied++
		bytes += n
	}

	c.JSON(http.StatusOK, gin.H{
		"message":       fmt.Sprintf("Replicated %d object(s) to %s", copied, req.Target),
		"target":        req.Target,
		"copied":        copied,
		"skipped":       skipped,
		"failed":        failed,
		"bytes_copied":  bytes,
		"total_objects": len(objects),
	})
}
