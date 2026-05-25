package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

func (s *Server) listSystemFiles(c *gin.Context) {
	path := c.DefaultQuery("path", "/")

	filesList, err := s.systemFilesManager.List(path)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"files": filesList,
		"path":  path,
	})
}

func (s *Server) getSystemFile(c *gin.Context) {
	path := c.Param("path")

	if c.Query("info") == "true" {
		info, err := s.systemFilesManager.GetFileInfo(path)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, info)
		return
	}

	if c.Query("list") == "true" {
		filesList, err := s.systemFilesManager.List(path)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"files": filesList,
			"path":  path,
		})
		return
	}

	file, info, err := s.systemFilesManager.ReadFile(path)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	defer file.Close()

	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", info.Name))
	c.Header("Content-Length", fmt.Sprintf("%d", info.Size))
	c.DataFromReader(http.StatusOK, info.Size, "application/octet-stream", file, nil)
}

func (s *Server) uploadSystemFile(c *gin.Context) {
	path := c.Param("path")

	file, _, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file provided"})
		return
	}
	defer file.Close()

	if err := s.systemFilesManager.WriteFile(path, file); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	info, _ := s.systemFilesManager.GetFileInfo(path)
	c.JSON(http.StatusOK, gin.H{
		"message": "File uploaded successfully",
		"file":    info,
	})
}

func (s *Server) deleteSystemFile(c *gin.Context) {
	path := c.Param("path")

	if err := s.systemFilesManager.DeleteFile(path); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Deleted successfully"})
}

func (s *Server) createSystemDir(c *gin.Context) {
	path := c.Param("path")

	if err := s.systemFilesManager.CreateDirectory(path); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	info, _ := s.systemFilesManager.GetFileInfo(path)
	c.JSON(http.StatusOK, gin.H{
		"message":   "Directory created",
		"directory": info,
	})
}

func (s *Server) createSystemFile(c *gin.Context) {
	path := c.Param("path")

	if err := s.systemFilesManager.CreateFile(path); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	info, _ := s.systemFilesManager.GetFileInfo(path)
	c.JSON(http.StatusOK, gin.H{
		"message": "File created",
		"file":    info,
	})
}

func (s *Server) chmodSystemFile(c *gin.Context) {
	path := c.Param("path")

	var req struct {
		Mode int `json:"mode" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	if req.Mode < 0 || req.Mode > 0o777 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mode must be between 0 and 0777"})
		return
	}

	if err := s.systemFilesManager.Chmod(path, os.FileMode(req.Mode)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	info, _ := s.systemFilesManager.GetFileInfo(path)
	c.JSON(http.StatusOK, gin.H{
		"message": "Permissions updated",
		"file":    info,
	})
}

func (s *Server) getSystemFilesInfo(c *gin.Context) {
	path := c.DefaultQuery("path", "/")
	skipUsage := c.Query("usage") == "false"

	homePath := "/"
	if home, err := os.UserHomeDir(); err == nil {
		root := s.systemFilesManager.Root()
		if rel, err := filepath.Rel(root, home); err == nil && !strings.HasPrefix(rel, "..") {
			if rel == "." {
				homePath = "/"
			} else {
				homePath = "/" + filepath.ToSlash(rel)
			}
		}
	}

	response := gin.H{
		"root":      s.systemFilesManager.Root(),
		"home_path": homePath,
		"path":      path,
	}
	if !skipUsage {
		totalSize, fileCount, err := s.systemFilesManager.GetDiskUsage(path)
		if err != nil {
			totalSize = 0
			fileCount = 0
		}
		response["total_size"] = totalSize
		response["file_count"] = fileCount
	}

	c.JSON(http.StatusOK, response)
}
