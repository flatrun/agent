package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/flatrun/agent/internal/workspace"
	"github.com/gin-gonic/gin"
)

const maxWorkspaceLogoBytes = 512 << 10

var logoExtensions = map[string]string{
	"image/png":     ".png",
	"image/webp":    ".webp",
	"image/svg+xml": ".svg",
}

func (s *Server) workspaceLogoDir(slug string) string {
	return filepath.Join(s.config.DeploymentsPath, ".flatrun", "workspaces", slug)
}

func (s *Server) workspaceLogoFile(slug string) (string, string, os.FileInfo) {
	for contentType, ext := range logoExtensions {
		path := filepath.Join(s.workspaceLogoDir(slug), "logo"+ext)
		if info, err := os.Stat(path); err == nil {
			return path, contentType, info
		}
	}
	return "", "", nil
}

// withLogo sets the logo version, which changes whenever the logo is replaced.
func (s *Server) withLogo(m workspace.Membership) workspace.Membership {
	if _, _, info := s.workspaceLogoFile(m.Slug); info != nil {
		m.Logo = strconv.FormatInt(info.ModTime().UnixNano(), 36)
	}
	return m
}

func logoContentType(data []byte) string {
	detected := http.DetectContentType(data)
	if _, ok := logoExtensions[detected]; ok {
		return detected
	}
	head := bytes.TrimSpace(data[:min(len(data), 512)])
	if bytes.HasPrefix(head, []byte("<svg")) || (bytes.HasPrefix(head, []byte("<?xml")) && bytes.Contains(head, []byte("<svg"))) {
		return "image/svg+xml"
	}
	return ""
}

func (s *Server) putWorkspaceLogo(c *gin.Context) {
	w, role, ok := s.loadWorkspace(c, workspace.RoleAdmin)
	if !ok {
		return
	}
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "send the image as the file field of a form"})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxWorkspaceLogoBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(data) > maxWorkspaceLogoBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "a logo can be at most 512 KB"})
		return
	}
	contentType := logoContentType(data)
	if contentType == "" {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "a logo must be PNG, WebP or SVG"})
		return
	}
	dir := s.workspaceLogoDir(w.Slug)
	if err := os.MkdirAll(dir, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, ext := range logoExtensions {
		_ = os.Remove(filepath.Join(dir, "logo"+ext))
	}
	if err := os.WriteFile(filepath.Join(dir, "logo"+logoExtensions[contentType]), data, 0644); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, s.withLogo(workspace.Membership{Workspace: *w, Role: role}))
}

func (s *Server) getWorkspaceLogo(c *gin.Context) {
	w, _, ok := s.loadWorkspace(c, workspace.RoleMember)
	if !ok {
		return
	}
	path, contentType, _ := s.workspaceLogoFile(w.Slug)
	if path == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "this workspace has no logo"})
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// An SVG opened on its own would run any script inside it.
	c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, contentType, data)
}

func (s *Server) deleteWorkspaceLogo(c *gin.Context) {
	w, _, ok := s.loadWorkspace(c, workspace.RoleAdmin)
	if !ok {
		return
	}
	for _, ext := range logoExtensions {
		if err := os.Remove(filepath.Join(s.workspaceLogoDir(w.Slug), "logo"+ext)); err != nil && !errors.Is(err, os.ErrNotExist) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	c.Status(http.StatusNoContent)
}
