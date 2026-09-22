package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flatrun/agent/internal/access"
	"github.com/flatrun/agent/internal/docker"
	"github.com/gin-gonic/gin"
)

func TestApplicationAccessCheckUsesTheVisitorHTTPBoundary(t *testing.T) {
	base := t.TempDir()
	deploymentPath := filepath.Join(base, "private-app")
	if err := os.MkdirAll(deploymentPath, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := `name: private-app
type: web
domains:
  - id: private
    service: web
    container_port: 80
    domain: private.example.com
    access:
      enabled: true
      mode: allowlist
      allowed_emails:
        - person@example.com
      email_target_id: smtp
`
	if err := os.WriteFile(filepath.Join(deploymentPath, "service.yml"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deploymentPath, "docker-compose.yml"), []byte("services:\n  web:\n    image: nginx:alpine\n"), 0644); err != nil {
		t.Fatal(err)
	}
	accessService, err := access.New(base)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{manager: docker.NewManager(base), access: accessService}
	router := gin.New()
	router.GET("/api/access/check", server.checkApplicationAccess)
	router.GET("/api/access/verify", server.verifyApplicationAccess)

	request := httptest.NewRequest(http.MethodGet, "/api/access/check", nil)
	request.Header.Set("X-Original-Host", "private.example.com")
	request.Header.Set("X-Original-URI", "/")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous response = %d", response.Code)
	}

	link, err := accessService.MagicLink("person@example.com", "private.example.com", "/")
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/access/verify?token="+link, nil)
	request.Host = "private.example.com"
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusFound || len(response.Result().Cookies()) != 1 {
		t.Fatalf("verification response = %d, cookies = %v", response.Code, response.Result().Cookies())
	}
	cookie := response.Result().Cookies()[0]
	request = httptest.NewRequest(http.MethodGet, "/api/access/check", nil)
	request.Header.Set("X-Original-Host", "private.example.com")
	request.Header.Set("X-Original-URI", "/")
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("verified response = %d, body = %s", response.Code, response.Body.String())
	}
	restarted, err := access.New(base)
	if err != nil {
		t.Fatal(err)
	}
	server.access = restarted
	request = httptest.NewRequest(http.MethodGet, "/api/access/verify?token="+link, nil)
	request.Host = "private.example.com"
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("replayed verification response = %d", response.Code)
	}
}

func TestApplicationAccessLoginRejectsUnsafeReturnPath(t *testing.T) {
	server := &Server{}
	router := gin.New()
	router.GET("/api/access/login", server.applicationAccessLogin)
	request := httptest.NewRequest(http.MethodGet, "/api/access/login?return=/%5Cother.example.com", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `name="return" value="/"`) {
		t.Fatalf("unsafe return path was accepted: %d %s", response.Code, response.Body.String())
	}
}
