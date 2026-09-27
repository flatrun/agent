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
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

func TestDeploymentBackupDestinationsThroughHTTP(t *testing.T) {
	root := t.TempDir()
	deploymentDir := filepath.Join(root, "app")
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deploymentDir, "docker-compose.yml"), []byte("services:\n  app:\n    image: nginx\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deploymentDir, "service.yml"), []byte("name: app\n"), 0644); err != nil {
		t.Fatal(err)
	}
	disabled := false
	server := &Server{
		manager: docker.NewManager(root),
		config: &config.Config{Backup: config.BackupConfig{Destinations: []config.BackupDestination{
			{Name: "primary", Kind: "external", CredentialID: "private-credential"},
			{Name: "disabled", Kind: "external", Enabled: &disabled},
		}}},
	}
	router := gin.New()
	router.GET("/deployments/:name/backup-destinations", server.listDeploymentBackupDestinationOptions)
	router.PUT("/deployments/:name/backup-config", server.updateDeploymentBackupConfig)

	options := httptest.NewRecorder()
	router.ServeHTTP(options, httptest.NewRequest(http.MethodGet, "/deployments/app/backup-destinations", nil))
	if options.Code != http.StatusOK || strings.Contains(options.Body.String(), "private-credential") || strings.Contains(options.Body.String(), "disabled") {
		t.Fatalf("options response = %d %s", options.Code, options.Body.String())
	}

	unknown := httptest.NewRecorder()
	router.ServeHTTP(unknown, httptest.NewRequest(http.MethodPut, "/deployments/app/backup-config", strings.NewReader(`{"destinations":["missing"]}`)))
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown destination status = %d: %s", unknown.Code, unknown.Body.String())
	}

	saved := httptest.NewRecorder()
	router.ServeHTTP(saved, httptest.NewRequest(http.MethodPut, "/deployments/app/backup-config", strings.NewReader(`{"destinations":["primary"]}`)))
	if saved.Code != http.StatusOK {
		t.Fatalf("save status = %d: %s", saved.Code, saved.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(deploymentDir, "service.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata models.ServiceMetadata
	if err := yaml.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Backup == nil || len(metadata.Backup.Destinations) != 1 || metadata.Backup.Destinations[0] != "primary" {
		t.Fatalf("saved backup config = %#v", metadata.Backup)
	}

	var response struct {
		BackupConfig models.BackupSpec `json:"backup_config"`
	}
	if err := json.Unmarshal(saved.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.BackupConfig.Destinations) != 1 || response.BackupConfig.Destinations[0] != "primary" {
		t.Fatalf("response backup config = %#v", response.BackupConfig)
	}
}
