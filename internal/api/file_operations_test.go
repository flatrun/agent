package api

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/flatrun/agent/internal/files"
	"github.com/gin-gonic/gin"
)

func systemFileOperationsRouter(root string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	server := &Server{systemFilesManager: files.NewSystemManager(root)}
	router := gin.New()
	router.POST("/copy", server.copySystemPath)
	router.POST("/move", server.moveSystemPath)
	router.GET("/archive", server.listSystemArchive)
	router.POST("/extract", server.extractSystemArchive)
	return router
}

func requestFileOperation(t *testing.T, router http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func TestSystemFileCopyAndMoveThroughHTTP(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "source"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source", "note.txt"), []byte("note"), 0644); err != nil {
		t.Fatal(err)
	}
	router := systemFileOperationsRouter(root)

	response := requestFileOperation(t, router, http.MethodPost, "/copy", `{
		"source_path":"/source/note.txt",
		"destination_path":"/copied/note.txt"
	}`)
	if response.Code != http.StatusOK {
		t.Fatalf("copy status = %d, body = %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "copied", "note.txt")); err != nil {
		t.Fatal(err)
	}

	response = requestFileOperation(t, router, http.MethodPost, "/move", `{
		"source_path":"/copied/note.txt",
		"destination_path":"/moved/note.txt"
	}`)
	if response.Code != http.StatusOK {
		t.Fatalf("move status = %d, body = %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "copied", "note.txt")); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "moved", "note.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestSystemArchiveViewAndExtractThroughHTTP(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "bundle.zip")
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(archive)
	entry, err := writer.Create("config/app.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("setting")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	router := systemFileOperationsRouter(root)

	response := requestFileOperation(t, router, http.MethodGet, "/archive?path=/bundle.zip", "")
	if response.Code != http.StatusOK {
		t.Fatalf("archive status = %d, body = %s", response.Code, response.Body.String())
	}
	if !bytes.Contains(response.Body.Bytes(), []byte(`"name":"config/app.txt"`)) {
		t.Fatalf("archive response = %s", response.Body.String())
	}

	response = requestFileOperation(t, router, http.MethodPost, "/extract", `{
		"source_path":"/bundle.zip",
		"destination_path":"/bundle"
	}`)
	if response.Code != http.StatusOK {
		t.Fatalf("extract status = %d, body = %s", response.Code, response.Body.String())
	}
	content, err := os.ReadFile(filepath.Join(root, "bundle", "config", "app.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "setting" {
		t.Fatalf("extracted content = %q", content)
	}
}
