package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
)

func TestDeployDeploymentRejectsUnsupportedAction(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := &Server{}
	router := gin.New()
	router.POST("/deployments/:name/deploy", server.deployDeployment)

	req := httptest.NewRequest(http.MethodPost, "/deployments/web/deploy", bytes.NewBufferString(`{"action":"delete"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeployDeploymentRejectsInvalidJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := &Server{}
	router := gin.New()
	router.POST("/deployments/:name/deploy", server.deployDeployment)

	req := httptest.NewRequest(http.MethodPost, "/deployments/web/deploy", bytes.NewBufferString(`{`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAddDeploymentComposeMountPersistsCompose(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	deployDir := filepath.Join(tmpDir, "web")
	if err := os.MkdirAll(deployDir, 0755); err != nil {
		t.Fatalf("mkdir deployment: %v", err)
	}
	compose := `name: web
services:
  app:
    image: nginx:alpine
    networks:
      - proxy
networks:
  proxy:
    external: true
`
	if err := os.WriteFile(filepath.Join(deployDir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatalf("write compose: %v", err)
	}

	server := &Server{
		config: &config.Config{
			Infrastructure: config.InfrastructureConfig{DefaultProxyNetwork: "proxy"},
		},
		manager: docker.NewManager(tmpDir),
	}
	router := gin.New()
	router.POST("/deployments/:name/compose/mount", server.addDeploymentComposeMount)

	req := httptest.NewRequest(http.MethodPost, "/deployments/web/compose/mount", bytes.NewBufferString(`{
		"source_path": "/config/nginx.conf",
		"target_path": "/etc/nginx/conf.d/default.conf",
		"service_name": "app",
		"read_only": true
	}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	updated, err := os.ReadFile(filepath.Join(deployDir, "docker-compose.yml"))
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	if !strings.Contains(string(updated), `./config/nginx.conf:/etc/nginx/conf.d/default.conf:ro`) {
		t.Fatalf("compose missing mount:\n%s", string(updated))
	}
}

func TestAddDeploymentComposeMountAppliesSELinuxRelabel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	deployDir := filepath.Join(tmpDir, "web")
	if err := os.MkdirAll(deployDir, 0755); err != nil {
		t.Fatalf("mkdir deployment: %v", err)
	}
	compose := `name: web
services:
  app:
    image: nginx:alpine
    networks:
      - proxy
networks:
  proxy:
    external: true
`
	if err := os.WriteFile(filepath.Join(deployDir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatalf("write compose: %v", err)
	}

	server := &Server{
		config: &config.Config{
			Infrastructure: config.InfrastructureConfig{DefaultProxyNetwork: "proxy"},
		},
		manager: docker.NewManager(tmpDir),
	}
	router := gin.New()
	router.POST("/deployments/:name/compose/mount", server.addDeploymentComposeMount)

	req := httptest.NewRequest(http.MethodPost, "/deployments/web/compose/mount", bytes.NewBufferString(`{
		"source_path": "/data",
		"target_path": "/var/lib/data",
		"service_name": "app",
		"read_only": true,
		"selinux": "Z"
	}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	updated, err := os.ReadFile(filepath.Join(deployDir, "docker-compose.yml"))
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	if !strings.Contains(string(updated), `./data:/var/lib/data:ro,Z`) {
		t.Fatalf("compose missing selinux-relabelled mount:\n%s", string(updated))
	}
}

func TestAddDeploymentComposeMountRejectsInvalidSELinux(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := &Server{}
	router := gin.New()
	router.POST("/deployments/:name/compose/mount", server.addDeploymentComposeMount)

	req := httptest.NewRequest(http.MethodPost, "/deployments/web/compose/mount", bytes.NewBufferString(`{
		"source_path": "/data",
		"target_path": "/data",
		"service_name": "app",
		"selinux": "bogus"
	}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAddDeploymentComposeMountRejectsTraversal(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := &Server{}
	router := gin.New()
	router.POST("/deployments/:name/compose/mount", server.addDeploymentComposeMount)

	req := httptest.NewRequest(http.MethodPost, "/deployments/web/compose/mount", bytes.NewBufferString(`{
		"source_path": "../secret",
		"target_path": "/secret",
		"service_name": "app"
	}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
