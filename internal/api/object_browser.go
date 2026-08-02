package api

import (
	"fmt"
	"net/http"
	"path"

	"github.com/flatrun/agent/internal/backup"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
)

// storeS3ByName resolves a registered object store by name into a usable S3
// client (plus its destination record), writing an error response and returning
// false on failure.
func (s *Server) storeS3ByName(c *gin.Context) (*backup.S3Store, config.BackupDestination, bool) {
	dest, ok := s.findDestinationByName(c.Param("name"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "object store not found"})
		return nil, config.BackupDestination{}, false
	}
	store, err := s.buildStore(dest)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return nil, config.BackupDestination{}, false
	}
	s3store, ok := store.(*backup.S3Store)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store is not S3-compatible"})
		return nil, config.BackupDestination{}, false
	}
	return s3store, dest, true
}

// scopedStore returns the store scoped to the request's target bucket, which is
// the ?bucket query when given, otherwise the store's configured bucket.
func scopedStore(store *backup.S3Store, dest config.BackupDestination, c *gin.Context) *backup.S3Store {
	bucket := c.Query("bucket")
	if bucket == "" {
		bucket = dest.Bucket
	}
	return store.WithBucket(bucket)
}

// listStoreBuckets lists every bucket on the store's server.
func (s *Server) listStoreBuckets(c *gin.Context) {
	store, dest, ok := s.storeS3ByName(c)
	if !ok {
		return
	}
	buckets, err := store.ListBuckets(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"buckets": buckets, "backup_bucket": dest.Bucket})
}

// createStoreBucket creates a new bucket on the store's server.
func (s *Server) createStoreBucket(c *gin.Context) {
	store, _, ok := s.storeS3ByName(c)
	if !ok {
		return
	}
	var req struct {
		Bucket string `json:"bucket" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := store.WithBucket(req.Bucket).EnsureBucket(c.Request.Context()); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"message": "Bucket created", "bucket": req.Bucket})
}

// listStoreObjects returns every object in the target bucket, optionally
// filtered by a key prefix.
func (s *Server) listStoreObjects(c *gin.Context) {
	store, dest, ok := s.storeS3ByName(c)
	if !ok {
		return
	}
	objects, err := scopedStore(store, dest, c).ListObjects(c.Request.Context(), c.Query("prefix"))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if objects == nil {
		objects = []backup.ObjectInfo{}
	}
	c.JSON(http.StatusOK, gin.H{"objects": objects})
}

// uploadStoreObject stores an uploaded file at the given key (defaulting to the
// file's name) in the target bucket.
func (s *Server) uploadStoreObject(c *gin.Context) {
	store, dest, ok := s.storeS3ByName(c)
	if !ok {
		return
	}
	target := scopedStore(store, dest, c)
	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is required"})
		return
	}
	key := c.PostForm("key")
	if key == "" {
		key = fileHeader.Filename
	}
	f, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defer f.Close()

	if err := target.Put(c.Request.Context(), key, f, fileHeader.Size); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"message": "Object uploaded", "key": key})
}

// downloadStoreObject streams an object back to the caller as an attachment.
func (s *Server) downloadStoreObject(c *gin.Context) {
	store, dest, ok := s.storeS3ByName(c)
	if !ok {
		return
	}
	key := c.Query("key")
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "key is required"})
		return
	}
	reader, err := scopedStore(store, dest, c).Open(c.Request.Context(), key)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	defer reader.Close()

	if c.Query("inline") == "true" {
		c.DataFromReader(http.StatusOK, -1, "application/octet-stream", reader, nil)
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", path.Base(key)))
	c.DataFromReader(http.StatusOK, -1, "application/octet-stream", reader, nil)
}

// deleteStoreObject removes a single object from the target bucket. Deletes in
// the store's backup bucket are refused, so browsing cannot destroy backups;
// objects in any other bucket are freely removable.
func (s *Server) deleteStoreObject(c *gin.Context) {
	store, dest, ok := s.storeS3ByName(c)
	if !ok {
		return
	}
	bucket := c.Query("bucket")
	if bucket == "" {
		bucket = dest.Bucket
	}
	if bucket == dest.Bucket {
		c.JSON(http.StatusForbidden, gin.H{
			"error": fmt.Sprintf("deletes are disabled in the backup bucket %q to protect backups; use another bucket", dest.Bucket),
		})
		return
	}
	key := c.Query("key")
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "key is required"})
		return
	}
	if err := store.WithBucket(bucket).Delete(c.Request.Context(), key); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Object deleted", "key": key})
}
