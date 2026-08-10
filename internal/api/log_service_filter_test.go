package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flatrun/agent/internal/docker"
	"github.com/gin-gonic/gin"
)

func writeLogFilterDeployment(t *testing.T) (base string, name string) {
	t.Helper()

	base = t.TempDir()
	name = "log-filter-app"
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	compose := "name: " + name + `
services:
  web:
    image: nginx:alpine
  worker:
    image: alpine:latest
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}
	return base, name
}

// A service name reaches compose as an argument, so it is checked against the compose file
// before it gets there rather than passed through.
func TestDeploymentLogsRejectUnknownService(t *testing.T) {
	gin.SetMode(gin.TestMode)

	base, name := writeLogFilterDeployment(t)
	server := &Server{manager: docker.NewManager(base)}
	router := gin.New()
	router.GET("/deployments/:name/logs", server.getDeploymentLogs)

	req := httptest.NewRequest(http.MethodGet, "/deployments/"+name+"/logs?service=--rm", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown service, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	for _, want := range []string{"web", "worker"} {
		if !strings.Contains(resp.Error, want) {
			t.Errorf("error should name the available service %q, got %q", want, resp.Error)
		}
	}
}

// A file source is one file the deployment writes, so asking for a service alongside it is
// not an error: there is simply nothing per-service to narrow.
func TestDeploymentLogsAcceptServiceAlongsideFileSource(t *testing.T) {
	gin.SetMode(gin.TestMode)

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

	server := &Server{manager: docker.NewManager(base)}
	router := gin.New()
	router.GET("/deployments/:name/logs", server.getDeploymentLogs)

	req := httptest.NewRequest(http.MethodGet, "/deployments/"+name+"/logs?source=app-file&service=web", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Logs string `json:"logs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !strings.Contains(resp.Logs, "second line") {
		t.Errorf("expected the file's contents, got %q", resp.Logs)
	}
}
