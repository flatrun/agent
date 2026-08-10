package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/flatrun/agent/internal/docker"
	"github.com/gin-gonic/gin"
)

// A stream that answers 200 and then says nothing looks identical to one that has nothing to
// report yet, so a source that cannot be read has to fail before the status goes out.
func TestInternalLogStreamFailsBeforeAnsweringOK(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base, name := writeLogFilterDeployment(t)

	metadata := "log_sources:\n  - id: escape\n    name: Escape\n    type: file\n    path: ../outside.log\n"
	if err := os.WriteFile(filepath.Join(base, name, "service.yml"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}

	server := &Server{manager: docker.NewManager(base), pluginToken: "plugin-secret"}
	router := gin.New()
	router.GET("/internal/logs/stream", server.streamInternalLogs)

	req := httptest.NewRequest(http.MethodGet, "/internal/logs/stream?deployment="+name+"&source=escape", nil)
	req.Header.Set("X-Plugin-Token", "plugin-secret")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
