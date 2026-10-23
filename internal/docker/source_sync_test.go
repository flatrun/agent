package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncSourceTree_KeepsLocalState(t *testing.T) {
	base := t.TempDir()
	d := NewDiscovery(base)
	deployDir := filepath.Join(base, "app")
	files := map[string]string{
		"docker-compose.yml": "services:\n  app:\n    image: nginx\n    volumes:\n      - ./data:/data\n",
		".env":               "SECRET=local\n",
		"service.yml":        "name: app\n",
		"data/db.sqlite":     "live data",
		"index.html":         "old",
	}
	for rel, content := range files {
		p := filepath.Join(deployDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	src := t.TempDir()
	incoming := map[string]string{
		"docker-compose.yml": "services:\n  app:\n    image: httpd\n",
		".env":               "SECRET=from-repo\n",
		"data/db.sqlite":     "repo fixture",
		"index.html":         "new",
		"assets/app.js":      "js",
	}
	for rel, content := range incoming {
		p := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if err := d.SyncSourceTree("app", src); err != nil {
		t.Fatalf("SyncSourceTree: %v", err)
	}

	want := map[string]string{
		"docker-compose.yml": files["docker-compose.yml"],
		".env":               files[".env"],
		"data/db.sqlite":     files["data/db.sqlite"],
		"index.html":         "new",
		"assets/app.js":      "js",
	}
	for rel, content := range want {
		got, err := os.ReadFile(filepath.Join(deployDir, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if string(got) != content {
			t.Errorf("%s = %q, want %q", rel, got, content)
		}
	}
}

func TestCarryExternalNetworks(t *testing.T) {
	prev := `services:
  app:
    image: nginx
    networks:
      - proxy
      - internal
networks:
  proxy:
    external: true
  internal: {}
`
	next := "services:\n  app:\n    image: httpd\n  worker:\n    image: busybox\n"

	out, err := CarryExternalNetworks(prev, next)
	if err != nil {
		t.Fatalf("CarryExternalNetworks: %v", err)
	}
	compose, err := ParseComposeYAML(out)
	if err != nil {
		t.Fatal(err)
	}
	app := compose["services"].(map[string]interface{})["app"].(map[string]interface{})
	nets, _ := app["networks"].([]interface{})
	if len(nets) != 1 || nets[0] != "proxy" {
		t.Errorf("app networks = %v, want [proxy]", nets)
	}
	if strings.Contains(out, "internal") {
		t.Error("non-external networks must not be carried over")
	}
	if !strings.Contains(out, "image: httpd") {
		t.Error("new compose content was lost")
	}
}
