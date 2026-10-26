package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
	if bytes.Contains(data[:min(len(data), 512)], []byte("<svg")) {
		return "image/svg+xml"
	}
	return ""
}

// saveLogo stores the uploaded image in dir, replacing any earlier logo, and returns the file
// name it wrote. It answers the request itself when the upload is refused.
func saveLogo(c *gin.Context, dir string) (string, bool) {
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "send the image as the file field of a form"})
		return "", false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxLogoBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return "", false
	}
	if len(data) > maxLogoBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "a logo can be at most 512 KB"})
		return "", false
	}
	contentType := logoContentType(data)
	if contentType == "" {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "a logo must be PNG, WebP or SVG"})
		return "", false
	}
	if err := writeLogo(dir, logoExtensions[contentType], data); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return "", false
	}
	return "logo" + logoExtensions[contentType], true
}

// writeLogo replaces the logo only once the new file is complete, so a failed write keeps the
// old one.
func writeLogo(dir, ext string, data []byte) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "logo-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, "logo"+ext)); err != nil {
		return err
	}
	for _, other := range logoExtensions {
		if other != ext {
			_ = os.Remove(filepath.Join(dir, "logo"+other))
		}
	}
	return nil
}

func serveLogo(c *gin.Context, dir, missing string) {
	path, _, _ := logoFile(dir)
	if path == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": missing})
		return
	}
	serveLogoFile(c, path)
}

// containedFile resolves rel inside root, following symlinks, and refuses a path that leads
// outside it.
func containedFile(root, rel string) (string, bool) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	file, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "", false
	}
	if inside, err := filepath.Rel(realRoot, file); err != nil || inside == "." || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", false
	}
	return file, true
}

// serveLogoFile sends an image only when it is one a logo may be.
func serveLogoFile(c *gin.Context, path string) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxLogoBytes {
		c.JSON(http.StatusNotFound, gin.H{"error": "logo not found"})
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "logo not found"})
		return
	}
	contentType := logoContentType(data)
	if contentType == "" {
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
