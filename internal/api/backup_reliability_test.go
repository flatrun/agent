package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/backup"
	"github.com/flatrun/agent/internal/docker"
	"github.com/gin-gonic/gin"
)

func runFailingBackupThroughHTTP(t *testing.T, metadata, dockerScript string, setup func(string)) (backup.Job, string) {
	t.Helper()
	root := t.TempDir()
	deploymentDir := filepath.Join(root, "app")
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deploymentDir, "docker-compose.yml"), []byte("services:\n  web:\n    image: nginx\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deploymentDir, "service.yml"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(deploymentDir)
	}

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "docker.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + logPath + "\n" + dockerScript + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	backupManager, err := backup.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{manager: docker.NewManager(root), backupManager: backupManager}
	router := gin.New()
	router.POST("/deployments/:name/backups", server.createDeploymentBackup)
	router.GET("/deployments/:name/backups/jobs/:id", server.getBackupJob)

	created := httptest.NewRecorder()
	router.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/deployments/app/backups", nil))
	if created.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	var createResponse struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createResponse); err != nil {
		t.Fatal(err)
	}

	var job backup.Job
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/deployments/app/backups/jobs/"+createResponse.JobID, nil))
		var body struct {
			Job backup.Job `json:"job"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		job = body.Job
		if job.Status == backup.JobStatusFailed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job.Status != backup.JobStatusFailed {
		t.Fatalf("job status = %s, want failed", job.Status)
	}
	logBytes, _ := os.ReadFile(logPath)
	return job, string(logBytes)
}

func TestBackupRequiredDatabaseFailureThroughHTTP(t *testing.T) {
	metadata := "name: app\nbackup:\n  databases:\n    - service: db\n      type: unsupported\n  post_hooks:\n    - service: web\n      command: resume\n"
	job, dockerLog := runFailingBackupThroughHTTP(t, metadata, "exit 0", nil)
	if len(job.ComponentResults) == 0 || job.ComponentResults[len(job.ComponentResults)-1].Status != backup.ResultStatusFailed {
		t.Fatalf("component results = %#v", job.ComponentResults)
	}
	if !strings.Contains(dockerLog, "resume") {
		t.Fatalf("cleanup invocation = %q", dockerLog)
	}
}

func TestBackupPreparationFailureStillRunsCleanupThroughHTTP(t *testing.T) {
	metadata := "name: app\nbackup:\n  databases:\n    - service: db\n      type: unsupported\n  pre_hooks:\n    - service: web\n      command: prepare\n  post_hooks:\n    - service: web\n      command: resume\n"
	_, dockerLog := runFailingBackupThroughHTTP(t, metadata, "case \"$*\" in *prepare*) exit 1;; *) exit 0;; esac", nil)
	if !strings.Contains(dockerLog, "prepare") || !strings.Contains(dockerLog, "resume") {
		t.Fatalf("hook invocations = %q", dockerLog)
	}
}

func TestBackupRequiredFileFailureThroughHTTP(t *testing.T) {
	metadata := "name: app\nbackup:\n  databases:\n    - service: db\n      type: unsupported\n  post_hooks:\n    - service: web\n      command: resume\n"
	job, dockerLog := runFailingBackupThroughHTTP(t, metadata, "exit 0", func(deploymentDir string) {
		dataDir := filepath.Join(deploymentDir, "data")
		if err := os.MkdirAll(dataDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dataDir, "missing"), filepath.Join(dataDir, "broken")); err != nil {
			t.Fatal(err)
		}
	})
	fileFailed := false
	for _, result := range job.ComponentResults {
		if result.Kind == "files" && result.Status == backup.ResultStatusFailed {
			fileFailed = true
		}
	}
	if !fileFailed {
		t.Fatalf("component results = %#v", job.ComponentResults)
	}
	if !strings.Contains(dockerLog, "resume") {
		t.Fatalf("cleanup invocation = %q", dockerLog)
	}
}
