package docker

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/models"
)

func TestRenderOverlayEscapesInterpolationAndSkipsEmptyServices(t *testing.T) {
	out, err := RenderOverlay(map[string]models.ServiceSettings{
		"web": {
			Resources:   &models.ServiceResources{CPUs: "0.5", Memory: "512M"},
			Restart:     "unless-stopped",
			Logging:     &models.ServiceLogging{MaxSize: "10m", MaxFiles: 3},
			Environment: map[string]string{"PRICE": "$5"},
			Command:     []string{"php", "-d", "memory_limit=$LIMIT"},
		},
		"worker": {},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := overlayHeader + `services:
  web:
    deploy:
      resources:
        limits:
          cpus: "0.5"
          memory: 512m
    restart: unless-stopped
    logging:
      driver: json-file
      options:
        max-file: "3"
        max-size: 10m
    environment:
      PRICE: $$5
    command:
      - php
      - -d
      - memory_limit=$$LIMIT
`
	if string(out) != want {
		t.Fatalf("overlay =\n%s\nwant\n%s", out, want)
	}
	if out, _ := RenderOverlay(map[string]models.ServiceSettings{"web": {}}); out != nil {
		t.Fatalf("settings that change nothing must not produce an overlay, got\n%s", out)
	}
}

func requireCompose(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose is not available")
	}
}

func overlayDeployment(t *testing.T) (*Manager, string) {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "shop")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	compose := "name: shop\nservices:\n  web:\n    image: nginx:alpine\n    environment:\n      KEEP: base\n  worker:\n    image: busybox\n"
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}
	return NewManager(base), dir
}

func TestServiceSettingsReachComposeThroughTheOverlay(t *testing.T) {
	requireCompose(t)
	m, dir := overlayDeployment(t)

	err := m.ApplyServiceSettings("shop", map[string]models.ServiceSettings{
		"web": {Resources: &models.ServiceResources{Memory: "256m"}, Environment: map[string]string{"PRICE": "$5"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := m.executor.runCompose(dir, nil, "config", "--format", "json")
	if err != nil {
		t.Fatalf("compose config: %v\n%s", err, out)
	}
	var merged struct {
		Services map[string]struct {
			Environment map[string]string `json:"environment"`
			Deploy      struct {
				Resources struct {
					Limits struct {
						Memory json.Number `json:"memory"`
					} `json:"limits"`
				} `json:"resources"`
			} `json:"deploy"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(out), &merged); err != nil {
		t.Fatal(err)
	}
	web := merged.Services["web"]
	if web.Deploy.Resources.Limits.Memory.String() != "268435456" {
		t.Errorf("memory limit = %q, want 256 MiB", web.Deploy.Resources.Limits.Memory)
	}
	// compose config prints a literal $ escaped as $$, the same way it reads one.
	if web.Environment["PRICE"] != "$$5" || web.Environment["KEEP"] != "base" {
		t.Errorf("environment = %v, want the literal value merged with the compose file's own", web.Environment)
	}
}

func TestRejectedOverlayKeepsTheInstalledOne(t *testing.T) {
	requireCompose(t)
	m, dir := overlayDeployment(t)
	good := map[string]models.ServiceSettings{"web": {Restart: "always"}}
	if err := m.ApplyServiceSettings("shop", good); err != nil {
		t.Fatal(err)
	}
	installed, _ := os.ReadFile(filepath.Join(dir, models.ComposeOverlayFile))

	err := m.ApplyServiceSettings("shop", map[string]models.ServiceSettings{"ghost": {Restart: "always"}})
	var invalid *OverlayValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("a service missing from the compose file must be refused by compose, got %v", err)
	}
	now, _ := os.ReadFile(filepath.Join(dir, models.ComposeOverlayFile))
	if string(now) != string(installed) {
		t.Fatalf("the installed overlay changed after a rejected one:\n%s", now)
	}
	if _, err := os.Stat(filepath.Join(dir, models.ComposeOverlayFile+".new")); !os.IsNotExist(err) {
		t.Fatal("the rejected candidate must be removed")
	}
}

func TestOverlayIsNeverTakenForTheDeploymentsComposeFile(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		models.ComposeOverlayFile: "services: {}\n",
		"app-compose.yml":         "services:\n  web:\n    image: nginx\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if got := FindComposeFile(dir); filepath.Base(got) != "app-compose.yml" {
		t.Fatalf("FindComposeFile = %s", got)
	}
	if got := (&ComposeExecutor{}).findComposeFile(dir); !strings.HasSuffix(got, "app-compose.yml") {
		t.Fatalf("findComposeFile = %s", got)
	}
}
