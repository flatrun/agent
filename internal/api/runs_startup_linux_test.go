package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/runs"
	"github.com/flatrun/agent/internal/runtimes"
	"github.com/flatrun/agent/internal/setup"
	"github.com/flatrun/agent/pkg/config"
	"github.com/google/uuid"
)

func TestRunsServerStartupDisabledFirecracker(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Docker")
	}
	docker, err := runtimes.NewDocker()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := docker.Available(ctx); err != nil {
		t.Skipf("Docker unreachable: %v", err)
	}
	for _, state := range []string{"leftover", "absent", "unreadable"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			stateDir := filepath.Join(root, "state")
			pathHash := sha256.Sum256([]byte(root))
			scope := fmt.Sprintf("%x", pathHash[:12])
			scopeHash := sha256.Sum256([]byte(scope))
			scopeDir := filepath.Join(stateDir, fmt.Sprintf("%x", scopeHash[:6]))
			instance := filepath.Join(scopeDir, uuid.NewString())
			if state != "absent" {
				if err := os.MkdirAll(instance, 0700); err != nil {
					t.Fatal(err)
				}
				data, err := json.Marshal(map[string]string{"id": filepath.Base(instance), "scope": scope})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(instance, "state.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if state == "unreadable" {
				if err := os.Chmod(scopeDir, 0000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Chmod(scopeDir, 0700); err != nil {
						t.Error(err)
					}
				})
				if _, err := os.ReadDir(scopeDir); !os.IsPermission(err) {
					t.Skipf("requires directory permissions to deny access: %v", err)
				}
			}
			zero, disabled := 0, false
			cfg := &config.Config{
				DeploymentsPath: root,
				Auth:            config.AuthConfig{Enabled: true, JWTSecret: "startup-test-secret"},
				Runs: config.RunsConfig{Workers: 1, Firecracker: config.FirecrackerConfig{
					StateDir: stateDir, ChrootBaseDir: filepath.Join(root, "jailer"),
				}},
				Templates: config.TemplatesConfig{SyncInterval: &zero, GitHub: config.TemplatesGitHubConfig{Enabled: &disabled}},
			}
			configPath := filepath.Join(root, "config.yml")
			if err := config.Save(cfg, configPath); err != nil {
				t.Fatal(err)
			}
			if err := setup.NewManager(cfg, configPath).MarkComplete(); err != nil {
				t.Fatal(err)
			}
			t.Setenv("FLATRUN_ADMIN_PASSWORD", "admin-pass")
			s := New(cfg, configPath)
			httpServer := httptest.NewServer(s.Handler())
			s.server = httpServer.Config
			t.Cleanup(func() {
				httpServer.Close()
				if err := s.Stop(); err != nil {
					t.Error(err)
				}
				if s.schedulerManager != nil {
					s.schedulerManager.Stop()
				}
				if s.authManager != nil {
					_ = s.authManager.Close()
				}
				if s.auditManager != nil {
					_ = s.auditManager.Close()
				}
				if s.trafficManager != nil {
					_ = s.trafficManager.Close()
				}
			})
			if state == "leftover" {
				if _, err := os.Stat(instance); !os.IsNotExist(err) {
					t.Fatalf("leftover remains: %v", err)
				}
			}
			client := workspaceClient{t: t, url: httpServer.URL}.login("admin", "admin-pass")
			status, data := runBytes(t, client, "/runs/runtimes")
			var listing struct {
				Items []runs.RuntimeStatus `json:"items"`
			}
			if err := json.Unmarshal(data, &listing); err != nil || status != http.StatusOK || len(listing.Items) != 2 {
				t.Fatalf("runtimes: %d %s %v", status, data, err)
			}
			reason := "not configured on this node"
			if state == "unreadable" {
				reason = "firecracker recovery:"
			}
			if fc := listing.Items[1]; fc.Type != runtimes.TypeFirecracker || fc.Available || !strings.Contains(fc.Reason, reason) {
				t.Fatalf("firecracker: %+v", fc)
			}
			if status, body := client.do("POST", "/runs", "", map[string]any{"image": "busybox:latest", "runtime": "firecracker"}); status != http.StatusServiceUnavailable || !strings.Contains(body["error"].(string), reason) {
				t.Fatalf("firecracker submission: %d %v", status, body)
			}
			id := submitRun(t, client, map[string]any{"image": "busybox:latest", "runtime": "docker"})
			if status, body := client.do("DELETE", "/runs/"+id, "", nil); status != http.StatusOK {
				t.Fatalf("cancel Docker run: %d %v", status, body)
			}
		})
	}
}
