package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gin-gonic/gin"
)

const maxLogoBytes = 512 << 10

var logoExtensions = map[string]string{
	"image/png":     ".png",
	"image/webp":    ".webp",
	"image/svg+xml": ".svg",
}

func logoFile(dir string) (string, string, os.FileInfo) {
	for contentType, ext := range logoExtensions {
		path := filepath.Join(dir, "logo"+ext)
		if info, err := os.Stat(path); err == nil {
			return path, contentType, info
		}
	}
	return "", "", nil
}

// logoVersion changes whenever the logo in dir is replaced, and is empty when there is none.
func logoVersion(dir string) string {
	if _, _, info := logoFile(dir); info != nil {
		return strconv.FormatInt(info.ModTime().UnixNano(), 36)
	}
	return ""
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

// saveLogo stores the uploaded image in dir, replacing any earlier logo. It answers the
// request itself when the upload is refused.
func saveLogo(c *gin.Context, dir string) bool {
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "send the image as the file field of a form"})
		return false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxLogoBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return false
	}
	if len(data) > maxLogoBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "a logo can be at most 512 KB"})
		return false
	}
	contentType := logoContentType(data)
	if contentType == "" {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "a logo must be PNG, WebP or SVG"})
		return false
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return false
	}
	for _, ext := range logoExtensions {
		_ = os.Remove(filepath.Join(dir, "logo"+ext))
	}
	if err := os.WriteFile(filepath.Join(dir, "logo"+logoExtensions[contentType]), data, 0644); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return false
	}
	return true
}

func serveLogo(c *gin.Context, dir, missing string) {
	path, _, _ := logoFile(dir)
	if path == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": missing})
		return
	}
	serveLogoFile(c, path)
}

// serveLogoFile sends an image only when it is one a logo may be.
func serveLogoFile(c *gin.Context, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "logo not found"})
		return
	}
	contentType := logoContentType(data)
	if contentType == "" || len(data) > maxLogoBytes {
		c.JSON(http.StatusNotFound, gin.H{"error": "logo not found"})
		return
	}
	// An SVG opened on its own would run any script inside it.
	c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, contentType, data)
}

func removeLogo(c *gin.Context, dir string) {
	for _, ext := range logoExtensions {
		if err := os.Remove(filepath.Join(dir, "logo"+ext)); err != nil && !errors.Is(err, os.ErrNotExist) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	c.Status(http.StatusNoContent)
}
