package api

import (
	"fmt"
	"net/http"
	"path"

	"github.com/flatrun/agent/internal/backup"
	"github.com/gin-gonic/gin"
)

// storeS3ByName resolves a registered object store by name into a usable S3
// client, writing an error response and returning false on failure.
func (s *Server) storeS3ByName(c *gin.Context) (*backup.S3Store, bool) {
	dest, ok := s.findDestinationByName(c.Param("name"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "object store not found"})
		return nil, false
	}
	store, err := s.buildStore(dest)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return nil, false
	}
	s3store, ok := store.(*backup.S3Store)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store is not S3-compatible"})
		return nil, false
	}
	return s3store, true
}

// listStoreObjects returns every object in a store, optionally filtered by a
// key prefix.
func (s *Server) listStoreObjects(c *gin.Context) {
	store, ok := s.storeS3ByName(c)
	if !ok {
		return
	}
	objects, err := store.ListObjects(c.Request.Context(), c.Query("prefix"))
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
// file's name).
func (s *Server) uploadStoreObject(c *gin.Context) {
	store, ok := s.storeS3ByName(c)
	if !ok {
		return
	}
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

	if err := store.Put(c.Request.Context(), key, f, fileHeader.Size); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"message": "Object uploaded", "key": key})
}

// downloadStoreObject streams an object back to the caller as an attachment.
func (s *Server) downloadStoreObject(c *gin.Context) {
	store, ok := s.storeS3ByName(c)
	if !ok {
		return
	}
	key := c.Query("key")
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "key is required"})
		return
	}
	reader, err := store.Open(c.Request.Context(), key)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	defer reader.Close()

	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", path.Base(key)))
	c.DataFromReader(http.StatusOK, -1, "application/octet-stream", reader, nil)
}

// deleteStoreObject removes a single object from a store.
func (s *Server) deleteStoreObject(c *gin.Context) {
	store, ok := s.storeS3ByName(c)
	if !ok {
		return
	}
	key := c.Query("key")
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "key is required"})
		return
	}
	if err := store.Delete(c.Request.Context(), key); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Object deleted", "key": key})
}
