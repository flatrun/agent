package api

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/flatrun/agent/internal/auth"
)

func TestProjectLogo(t *testing.T) {
	s, client := setupWorkspaceTestServer(t)
	if _, err := s.authManager.CreateUser("bob", "", "bob-pass", auth.RoleOperator, nil); err != nil {
		t.Fatal(err)
	}
	admin := client.login("admin", "admin-pass")
	bob := client.login("bob", "bob-pass")
	if status, body := createDeploymentAs(admin, "", "shop"); status != http.StatusCreated {
		t.Fatalf("create = %d %v", status, body)
	}

	if status, _, _ := fetchLogo(t, admin, "/projects/shop/logo"); status != http.StatusNotFound {
		t.Fatalf("logo before upload = %d", status)
	}
	if status, _, _ := fetchLogo(t, admin, "/projects/missing/logo"); status != http.StatusNotFound {
		t.Fatalf("logo of a missing project = %d", status)
	}
	logo := pngBytes(t)
	if status := uploadLogo(t, bob, "/projects/shop/logo", logo); status != http.StatusForbidden {
		t.Fatalf("user without access uploading = %d", status)
	}
	if status := uploadLogo(t, admin, "/projects/shop/logo", []byte("not an image")); status != http.StatusUnsupportedMediaType {
		t.Fatalf("text upload = %d", status)
	}
	if status := uploadLogo(t, admin, "/projects/shop/logo", logo); status != http.StatusOK {
		t.Fatalf("upload = %d", status)
	}

	status, contentType, data := fetchLogo(t, admin, "/projects/shop/logo")
	if status != http.StatusOK || contentType != "image/png" || !bytes.Equal(data, logo) {
		t.Fatalf("reading logo = %d %s %d bytes", status, contentType, len(data))
	}
	if _, err := os.Stat(filepath.Join(s.manager.BasePath(), "shop", ".flatrun", "logo.png")); err != nil {
		t.Fatalf("logo not stored with the project: %v", err)
	}

	status, body := admin.do(http.MethodGet, "/projects", "", nil)
	items, _ := body["items"].([]any)
	if status != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["logo_version"] == nil {
		t.Fatalf("project list = %d %v", status, body)
	}
	status, body = admin.do(http.MethodGet, "/projects/shop", "", nil)
	project, _ := body["deployment"].(map[string]any)
	meta, _ := project["metadata"].(map[string]any)
	if status != http.StatusOK || project["logo_version"] == nil || meta["logo"] != "upload" {
		t.Fatalf("project = %d %v", status, body)
	}

	if status, _ := admin.do(http.MethodDelete, "/projects/shop/logo", "", nil); status != http.StatusNoContent {
		t.Fatalf("delete logo = %d", status)
	}
	if status, _, _ := fetchLogo(t, admin, "/projects/shop/logo"); status != http.StatusNotFound {
		t.Fatalf("logo after delete = %d", status)
	}
	status, body = admin.do(http.MethodGet, "/projects", "", nil)
	items, _ = body["items"].([]any)
	if status != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["logo_version"] != nil {
		t.Fatalf("project list after delete = %d %v", status, body)
	}
	status, body = admin.do(http.MethodGet, "/projects/shop", "", nil)
	project, _ = body["deployment"].(map[string]any)
	if meta, _ := project["metadata"].(map[string]any); status != http.StatusOK || meta["logo"] != nil {
		t.Fatalf("project after delete = %d %v", status, body)
	}
}

func TestProjectLogoSetting(t *testing.T) {
	_, client := setupWorkspaceTestServer(t)
	admin := client.login("admin", "admin-pass")
	if status, body := createDeploymentAs(admin, "", "shop"); status != http.StatusCreated {
		t.Fatalf("create = %d %v", status, body)
	}

	for _, logo := range []string{"simple-icons:redis", "logos:postgresql", "https://cdn.simpleicons.org/n8n", "template:trakli", ""} {
		if status, body := admin.do(http.MethodPut, "/projects/shop/metadata", "", map[string]string{"logo": logo}); status != http.StatusOK {
			t.Fatalf("logo %q = %d %v", logo, status, body)
		}
		_, body := admin.do(http.MethodGet, "/projects/shop", "", nil)
		project, _ := body["deployment"].(map[string]any)
		meta, _ := project["metadata"].(map[string]any)
		if got, _ := meta["logo"].(string); got != logo {
			t.Fatalf("logo %q stored as %q", logo, got)
		}
	}
	for _, logo := range []string{"redis", "javascript:alert(1)", "https://", "template:", "Simple Icons:Redis"} {
		if status, _ := admin.do(http.MethodPut, "/projects/shop/metadata", "", map[string]string{"logo": logo}); status != http.StatusBadRequest {
			t.Fatalf("logo %q = %d, want 400", logo, status)
		}
	}
	status, _ := admin.do(http.MethodPost, "/projects", "", map[string]any{
		"name":            "blog",
		"compose_content": "services:\n  web:\n    image: nginx:alpine\n",
		"metadata":        map[string]any{"name": "blog", "logo": "not a logo"},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("creating with an invalid logo = %d", status)
	}
}

func TestTemplateLogo(t *testing.T) {
	s, client := setupWorkspaceTestServer(t)
	admin := client.login("admin", "admin-pass")
	dir := filepath.Join(s.config.DeploymentsPath, ".flatrun", "templates", "trakli")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	logo := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 4 4"><rect width="4" height="4"/></svg>`)
	files := map[string]string{
		"docker-compose.yml": "services:\n  app:\n    image: ghcr.io/trakli/webservice:latest\n",
		"metadata.yml":       "name: Trakli\nlogo: logo.svg\n",
		"logo.svg":           string(logo),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	status, body := admin.do(http.MethodGet, "/templates", "", nil)
	var listed string
	if templates, ok := body["templates"].([]any); ok {
		for _, item := range templates {
			if tpl := item.(map[string]any); tpl["id"] == "trakli" {
				listed, _ = tpl["logo"].(string)
			}
		}
	}
	if status != http.StatusOK || listed != "/api/templates/trakli/logo" {
		t.Fatalf("template list = %d, logo %q, %v", status, listed, body)
	}

	anonymous := workspaceClient{t: t, url: client.url}
	status, contentType, data := fetchLogo(t, anonymous, "/templates/trakli/logo")
	if status != http.StatusOK || contentType != "image/svg+xml" || !bytes.Equal(data, logo) {
		t.Fatalf("template logo = %d %s %q", status, contentType, data)
	}
	if status, _, _ := fetchLogo(t, anonymous, "/templates/..%2F..%2Fconfig.yml/logo"); status != http.StatusNotFound {
		t.Fatalf("template outside the catalog = %d", status)
	}

	status, body = admin.do(http.MethodPost, "/projects", "", map[string]any{
		"name":            "money",
		"template_id":     "trakli",
		"compose_content": files["docker-compose.yml"],
	})
	if status != http.StatusCreated {
		t.Fatalf("create from template = %d %v", status, body)
	}
	_, body = admin.do(http.MethodGet, "/projects/money", "", nil)
	project, _ := body["deployment"].(map[string]any)
	if meta, _ := project["metadata"].(map[string]any); meta["template"] != "trakli" {
		t.Fatalf("project created from a template = %v", project["metadata"])
	}
}
