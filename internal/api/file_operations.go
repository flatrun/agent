package api

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

type fileOperationRequest struct {
	SourcePath      string `json:"source_path" binding:"required"`
	DestinationPath string `json:"destination_path" binding:"required"`
}

type filePushResult struct {
	Message     string `json:"message"`
	Destination string `json:"destination"`
	Deleted     bool   `json:"deleted"`
	Files       int    `json:"files"`
}

type filePushRequest struct {
	Archive     *multipart.FileHeader `json:"archive" form:"archive" binding:"required"`
	Destination string                `json:"destination" form:"destination" binding:"required"`
	Delete      bool                  `json:"delete" form:"delete"`
}

func (s *Server) copyDeploymentPath(c *gin.Context) {
	name := c.Param("name")
	if !s.requireUnprotectedDeploymentAction(c, name, protectedActionUploadFile) {
		return
	}
	var req fileOperationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source_path and destination_path are required"})
		return
	}
	if err := s.filesManager.Copy(name, req.SourcePath, req.DestinationPath); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, Message{Message: "Path copied"})
}

func (s *Server) moveDeploymentPath(c *gin.Context) {
	name := c.Param("name")
	if !s.requireUnprotectedDeploymentAction(c, name, protectedActionDeleteFile) {
		return
	}
	var req fileOperationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source_path and destination_path are required"})
		return
	}
	if err := s.filesManager.Rename(name, req.SourcePath, req.DestinationPath); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, Message{Message: "Path moved"})
}

func (s *Server) listDeploymentArchive(c *gin.Context) {
	entries, err := s.filesManager.ListArchive(c.Param("name"), c.Query("path"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, NewList(entries, "entries"))
}

func (s *Server) extractDeploymentArchive(c *gin.Context) {
	name := c.Param("name")
	if !s.requireUnprotectedDeploymentAction(c, name, protectedActionUploadFile) {
		return
	}
	var req fileOperationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source_path and destination_path are required"})
		return
	}
	if err := s.filesManager.ExtractArchive(name, req.SourcePath, req.DestinationPath); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, Message{Message: "Archive extracted"})
}

func (s *Server) pushDeploymentFiles(c *gin.Context) {
	name := c.Param("name")
	if !s.requireUnprotectedDeploymentAction(c, name, protectedActionUploadFile) {
		return
	}
	var req filePushRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "archive and destination are required"})
		return
	}
	archive, err := req.Archive.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "archive could not be opened"})
		return
	}
	defer archive.Close()

	format := filepath.Ext(req.Archive.Filename)
	if filepath.Ext(strings.TrimSuffix(req.Archive.Filename, format)) == ".tar" {
		format = ".tar" + format
	}
	temporary, err := os.CreateTemp("", "flatrun-push-*"+format)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not stage archive"})
		return
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := io.Copy(temporary, archive); err != nil {
		temporary.Close()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not stage archive"})
		return
	}
	if err := temporary.Close(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not stage archive"})
		return
	}

	count, err := s.filesManager.PushArchive(name, temporaryPath, req.Destination, req.Delete)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, Item[filePushResult]{Item: filePushResult{
		Message:     fmt.Sprintf("Pushed %d files", count),
		Destination: req.Destination,
		Deleted:     req.Delete,
		Files:       count,
	}})
}

func (s *Server) copySystemPath(c *gin.Context) {
	var req fileOperationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source_path and destination_path are required"})
		return
	}
	if err := s.systemFilesManager.Copy(req.SourcePath, req.DestinationPath); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, Message{Message: "Path copied"})
}

func (s *Server) moveSystemPath(c *gin.Context) {
	var req fileOperationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source_path and destination_path are required"})
		return
	}
	if err := s.systemFilesManager.Move(req.SourcePath, req.DestinationPath); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, Message{Message: "Path moved"})
}

func (s *Server) listSystemArchive(c *gin.Context) {
	entries, err := s.systemFilesManager.ListArchive(c.Query("path"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, NewList(entries, "entries"))
}

func (s *Server) extractSystemArchive(c *gin.Context) {
	var req fileOperationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source_path and destination_path are required"})
		return
	}
	if err := s.systemFilesManager.ExtractArchive(req.SourcePath, req.DestinationPath); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, Message{Message: "Archive extracted"})
}
