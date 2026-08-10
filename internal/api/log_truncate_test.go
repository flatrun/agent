package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flatrun/agent/internal/docker"
	"github.com/gin-gonic/gin"
)

func truncateRouter(t *testing.T, base string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	server := &Server{manager: docker.NewManager(base)}
	router := gin.New()
	router.DELETE("/deployments/:name/logs", server.deleteDeploymentLogs)
	return router
}

func TestDeleteDeploymentLogsEmptiesTheFile(t *testing.T) {
	base, name := writeLogFilterDeployment(t)
	logPath := filepath.Join(base, name, "app.log")
	if err := os.WriteFile(logPath, []byte("first line\nsecond line\n"), 0644); err != nil {
		t.Fatal(err)
	}
	metadata := `log_sources:
  - id: app-file
    name: App file
    type: file
    path: app.log
`
	if err := os.WriteFile(filepath.Join(base, name, "service.yml"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/deployments/"+name+"/logs?source=app-file", nil)
	w := httptest.NewRecorder()
	truncateRouter(t, base).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("the file should still exist so the application keeps writing to it: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("expected an empty file, got %d bytes", info.Size())
	}
}

// A source pointing outside the deployment is refused here as it is on read, so a crafted
// source cannot turn this into "truncate any file on the host".
func TestDeleteDeploymentLogsRefusesAnEscapingPath(t *testing.T) {
	base, name := writeLogFilterDeployment(t)
	outside := filepath.Join(base, "outside.log")
	if err := os.WriteFile(outside, []byte("not yours\n"), 0644); err != nil {
		t.Fatal(err)
	}
	metadata := "log_sources:\n  - id: escape\n    name: Escape\n    type: file\n    path: ../outside.log\n"
	if err := os.WriteFile(filepath.Join(base, name, "service.yml"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/deployments/"+name+"/logs?source=escape", nil)
	w := httptest.NewRecorder()
	truncateRouter(t, base).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if info, err := os.Stat(outside); err != nil || info.Size() == 0 {
		t.Errorf("the file outside the deployment must be untouched")
	}
}

// Emptying nothing at all should not read as success, which is what a mistyped service used to
// get: 200 with a count of zero.
func TestDeleteDeploymentLogsRejectsAnUnknownService(t *testing.T) {
	base, name := writeLogFilterDeployment(t)

	req := httptest.NewRequest(http.MethodDelete, "/deployments/"+name+"/logs?source=stdout&service=nope", nil)
	w := httptest.NewRecorder()
	truncateRouter(t, base).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "web") {
		t.Errorf("the error should name the services that do exist, got %s", w.Body.String())
	}
}

func TestDeleteDeploymentLogsRejectsAnUnknownSource(t *testing.T) {
	base, name := writeLogFilterDeployment(t)

	req := httptest.NewRequest(http.MethodDelete, "/deployments/"+name+"/logs?source=nope", nil)
	w := httptest.NewRecorder()
	truncateRouter(t, base).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
