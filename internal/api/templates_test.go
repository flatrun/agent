package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/networks"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
)

func setupTemplateTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()

	cfg := &config.Config{
		DeploymentsPath: tmpDir,
		API: config.APIConfig{
			Host: "0.0.0.0",
			Port: 8090,
		},
		Auth: config.AuthConfig{
			Enabled: false,
		},
		Infrastructure: config.InfrastructureConfig{
			DefaultProxyNetwork: "proxy",
		},
	}

	router := gin.New()
	authMiddleware := auth.NewMiddleware(&cfg.Auth)

	configPath := filepath.Join(tmpDir, "config.yml")
	_ = config.Save(cfg, configPath)

	s := &Server{
		config:          cfg,
		configPath:      configPath,
		router:          router,
		manager:         docker.NewManager(tmpDir),
		networksManager: networks.NewManager(),
		authMiddleware:  authMiddleware,
	}
	s.setupRoutes()
	return s, tmpDir
}

func writeTemplate(t *testing.T, baseDir, id, metadata, compose string) {
	t.Helper()
	dir := filepath.Join(baseDir, ".flatrun", "templates", id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("Failed to create template dir %s: %v", id, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.yml"), []byte(metadata), 0644); err != nil {
		t.Fatalf("Failed to write metadata for %s: %v", id, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatalf("Failed to write compose for %s: %v", id, err)
	}
}

func getTemplates(t *testing.T, s *Server, query string) []Template {
	t.Helper()
	path := "/api/templates"
	if query != "" {
		path += "?" + query
	}
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", path, nil)
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Templates []Template `json:"templates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	return resp.Templates
}

func TestListTemplates_ExcludesInfraByDefault(t *testing.T) {
	s, tmpDir := setupTemplateTestServer(t)

	writeTemplate(t, tmpDir, "wordpress", "name: WordPress\ncategory: application", "name: ${NAME}\nservices:\n  app:\n    image: wordpress\n")
	writeTemplate(t, tmpDir, "infra/nginx", "name: Nginx\ncategory: infrastructure\ntype: infrastructure", "name: ${NAME}\nservices:\n  nginx:\n    image: openresty/openresty:alpine\n")

	templates := getTemplates(t, s, "")
	for _, tmpl := range templates {
		if tmpl.ID == "infra/nginx" {
			t.Error("Default listing should not include infra templates")
		}
	}

	found := false
	for _, tmpl := range templates {
		if tmpl.ID == "wordpress" {
			found = true
		}
	}
	if !found {
		t.Error("Default listing should include app templates")
	}
}

func TestListTemplates_TypeInfrastructure(t *testing.T) {
	s, tmpDir := setupTemplateTestServer(t)

	writeTemplate(t, tmpDir, "wordpress", "name: WordPress\ncategory: application", "name: ${NAME}\nservices:\n  app:\n    image: wordpress\n")
	writeTemplate(t, tmpDir, "infra/nginx", "name: Nginx\ncategory: infrastructure\ntype: infrastructure", "name: ${NAME}\nservices:\n  nginx:\n    image: openresty/openresty:alpine\n")

	templates := getTemplates(t, s, "type=infrastructure")

	found := false
	for _, tmpl := range templates {
		if tmpl.ID == "infra/nginx" {
			found = true
		}
		if tmpl.ID == "wordpress" {
			t.Error("Infrastructure filter should not include app templates")
		}
	}
	if !found {
		t.Error("Infrastructure filter should include infra/nginx")
	}
}

func TestListTemplates_TypeAll(t *testing.T) {
	s, tmpDir := setupTemplateTestServer(t)

	writeTemplate(t, tmpDir, "wordpress", "name: WordPress\ncategory: application", "name: ${NAME}\nservices:\n  app:\n    image: wordpress\n")
	writeTemplate(t, tmpDir, "infra/nginx", "name: Nginx\ncategory: infrastructure\ntype: infrastructure", "name: ${NAME}\nservices:\n  nginx:\n    image: openresty/openresty:alpine\n")

	templates := getTemplates(t, s, "type=all")

	ids := map[string]bool{}
	for _, tmpl := range templates {
		ids[tmpl.ID] = true
	}
	if !ids["wordpress"] {
		t.Error("type=all should include wordpress")
	}
	if !ids["infra/nginx"] {
		t.Error("type=all should include infra/nginx")
	}
}

func TestListTemplates_RecursiveScan(t *testing.T) {
	s, tmpDir := setupTemplateTestServer(t)

	writeTemplate(t, tmpDir, "infra/nginx", "name: Nginx\ntype: infrastructure", "name: ${NAME}\nservices:\n  nginx:\n    image: nginx\n")
	writeTemplate(t, tmpDir, "infra/redis", "name: Redis\ntype: infrastructure", "name: ${NAME}\nservices:\n  redis:\n    image: redis\n")

	templates := getTemplates(t, s, "type=infrastructure")

	ids := map[string]bool{}
	for _, tmpl := range templates {
		ids[tmpl.ID] = true
	}
	if !ids["infra/nginx"] {
		t.Error("Should find infra/nginx")
	}
	if !ids["infra/redis"] {
		t.Error("Should find infra/redis")
	}
}

func TestGetInfraTemplateCompose(t *testing.T) {
	s, tmpDir := setupTemplateTestServer(t)

	compose := "name: ${NAME}\nservices:\n  nginx:\n    image: openresty/openresty:alpine\n"
	writeTemplate(t, tmpDir, "infra/nginx", "name: Nginx\ntype: infrastructure", compose)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/templates/infra/nginx/compose?name=my-nginx", nil)
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		TemplateID string `json:"template_id"`
		Name       string `json:"name"`
		Content    string `json:"content"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if resp.TemplateID != "infra/nginx" {
		t.Errorf("Expected template_id 'infra/nginx', got '%s'", resp.TemplateID)
	}
	if resp.Name != "my-nginx" {
		t.Errorf("Expected name 'my-nginx', got '%s'", resp.Name)
	}
	if resp.Content == "" {
		t.Error("Expected non-empty compose content")
	}
}

func TestGetFlatTemplateCompose(t *testing.T) {
	s, tmpDir := setupTemplateTestServer(t)

	compose := "name: ${NAME}\nservices:\n  app:\n    image: wordpress\n"
	writeTemplate(t, tmpDir, "wordpress", "name: WordPress\ncategory: application", compose)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/templates/wordpress/compose?name=my-wp", nil)
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		TemplateID string `json:"template_id"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.TemplateID != "wordpress" {
		t.Errorf("Expected template_id 'wordpress', got '%s'", resp.TemplateID)
	}
}

func TestGetInfraTemplateCompose_NotFound(t *testing.T) {
	s, _ := setupTemplateTestServer(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/templates/infra/nonexistent/compose?name=test", nil)
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetTemplateCompose_DirectoryTraversal(t *testing.T) {
	s, _ := setupTemplateTestServer(t)

	paths := []string{
		"/api/templates/..%2F..%2Fetc/compose?name=test",
		"/api/templates/infra/..%2F..%2Fetc/compose?name=test",
	}
	for _, p := range paths {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", p, nil)
		s.router.ServeHTTP(w, req)

		if w.Code == http.StatusOK {
			t.Errorf("Path %s should not return 200", p)
		}
	}
}
