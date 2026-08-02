package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/backup"
	"github.com/flatrun/agent/internal/credentials"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

func setupObjectStoreTestServer(t *testing.T) (*Server, *gin.Engine, func()) {
	gin.SetMode(gin.TestMode)

	tmpDir, err := os.MkdirTemp("", "objstore_test")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}

	cfg := &config.Config{
		DeploymentsPath: tmpDir,
		Auth: config.AuthConfig{
			Enabled:   true,
			JWTSecret: "test-jwt-secret-key-for-testing",
		},
	}
	os.Setenv("FLATRUN_ADMIN_PASSWORD", "testadminpass")

	authManager, err := auth.NewManager(tmpDir, &cfg.Auth, true)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("auth manager: %v", err)
	}

	backupManager, err := backup.NewManager(tmpDir)
	if err != nil {
		authManager.Close()
		os.RemoveAll(tmpDir)
		t.Fatalf("backup manager: %v", err)
	}

	server := &Server{
		config:             cfg,
		authManager:        authManager,
		credentialsManager: credentials.NewManager(tmpDir),
		backupManager:      backupManager,
	}

	router := gin.New()
	mw := auth.NewMiddlewareWithManager(&cfg.Auth, authManager)
	api := router.Group("/api")
	api.POST("/auth/login", mw.Login)

	protected := api.Group("")
	protected.Use(mw.RequireAuth())
	protected.GET("/storage-credentials", mw.RequirePermission(auth.PermBackupsRead), server.listStorageCredentials)
	protected.POST("/storage-credentials", mw.RequirePermission(auth.PermBackupsWrite), server.createStorageCredential)
	protected.PUT("/storage-credentials/:id", mw.RequirePermission(auth.PermBackupsWrite), server.updateStorageCredential)
	protected.DELETE("/storage-credentials/:id", mw.RequirePermission(auth.PermBackupsDelete), server.deleteStorageCredential)
	protected.GET("/backup-destinations", mw.RequirePermission(auth.PermBackupsRead), server.listBackupDestinations)
	protected.POST("/object-stores/provision-managed", mw.RequirePermission(auth.PermBackupsWrite), server.provisionManagedObjectStore)
	protected.GET("/object-stores/:name/objects", mw.RequirePermission(auth.PermBackupsRead), server.listStoreObjects)
	protected.POST("/object-stores/:name/objects", mw.RequirePermission(auth.PermBackupsWrite), server.uploadStoreObject)
	protected.GET("/object-stores/:name/objects/download", mw.RequirePermission(auth.PermBackupsRead), server.downloadStoreObject)
	protected.DELETE("/object-stores/:name/objects", mw.RequirePermission(auth.PermBackupsWrite), server.deleteStoreObject)

	cleanup := func() {
		authManager.Close()
		os.RemoveAll(tmpDir)
		os.Unsetenv("FLATRUN_ADMIN_PASSWORD")
	}
	return server, router, cleanup
}

func objStoreLogin(t *testing.T, router *gin.Engine) string {
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "testadminpass"})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return resp["token"].(string)
}

func osReq(t *testing.T, router *gin.Engine, method, path, token string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestStorageCredential_CreateListMaskDelete(t *testing.T) {
	_, router, cleanup := setupObjectStoreTestServer(t)
	defer cleanup()
	token := objStoreLogin(t, router)

	created := osReq(t, router, http.MethodPost, "/api/storage-credentials", token, map[string]any{
		"name": "prod-r2",
		"kind": "s3",
		"data": map[string]string{"access_key_id": "AKIATESTVALUE", "secret_access_key": "topsecret"},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var createdResp struct {
		Credential models.Credential `json:"credential"`
	}
	_ = json.Unmarshal(created.Body.Bytes(), &createdResp)
	id := createdResp.Credential.ID
	if id == "" {
		t.Fatal("expected credential id")
	}

	list := osReq(t, router, http.MethodGet, "/api/storage-credentials?kind=s3", token, nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list: %d", list.Code)
	}
	body := list.Body.String()
	if bytes.Contains([]byte(body), []byte("topsecret")) {
		t.Fatalf("secret leaked through the API: %s", body)
	}
	if !bytes.Contains([]byte(body), []byte(models.CredentialMask)) {
		t.Fatalf("expected masked secret in response: %s", body)
	}
	if !bytes.Contains([]byte(body), []byte("AKIATESTVALUE")) {
		t.Fatalf("access key id should be visible: %s", body)
	}

	upd := osReq(t, router, http.MethodPut, "/api/storage-credentials/"+id, token, map[string]any{
		"name": "prod-r2-renamed",
		"data": map[string]string{"secret_access_key": models.CredentialMask},
	})
	if upd.Code != http.StatusOK {
		t.Fatalf("update: %d %s", upd.Code, upd.Body.String())
	}

	del := osReq(t, router, http.MethodDelete, "/api/storage-credentials/"+id, token, nil)
	if del.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", del.Code, del.Body.String())
	}
}

func TestStorageCredential_DeleteInUseConflicts(t *testing.T) {
	server, router, cleanup := setupObjectStoreTestServer(t)
	defer cleanup()
	token := objStoreLogin(t, router)

	created := osReq(t, router, http.MethodPost, "/api/storage-credentials", token, map[string]any{
		"name": "in-use",
		"kind": "s3",
		"data": map[string]string{"access_key_id": "AKIATESTVALUE", "secret_access_key": "topsecret"},
	})
	var createdResp struct {
		Credential models.Credential `json:"credential"`
	}
	_ = json.Unmarshal(created.Body.Bytes(), &createdResp)
	id := createdResp.Credential.ID

	server.config.Backup.Destinations = []config.BackupDestination{
		{Name: "s3-prod", Type: "s3", Bucket: "b", CredentialID: id},
	}

	del := osReq(t, router, http.MethodDelete, "/api/storage-credentials/"+id, token, nil)
	if del.Code != http.StatusConflict {
		t.Fatalf("expected 409 for in-use credential, got %d %s", del.Code, del.Body.String())
	}
}

func TestProvisionManagedObjectStore_RequiresApiPort(t *testing.T) {
	_, router, cleanup := setupObjectStoreTestServer(t)
	defer cleanup()
	token := objStoreLogin(t, router)

	res := osReq(t, router, http.MethodPost, "/api/object-stores/provision-managed", token, map[string]any{
		"deployment": "some-store",
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without an api_port, got %d %s", res.Code, res.Body.String())
	}
}

func TestProvisionManagedObjectStore_MissingEnvReturns400(t *testing.T) {
	_, router, cleanup := setupObjectStoreTestServer(t)
	defer cleanup()
	token := objStoreLogin(t, router)

	res := osReq(t, router, http.MethodPost, "/api/object-stores/provision-managed", token, map[string]any{
		"deployment":     "no-such-store",
		"access_key_env": "MINIO_ROOT_USER",
		"secret_key_env": "MINIO_ROOT_PASSWORD",
		"api_port":       9000,
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a deployment with no environment, got %d %s", res.Code, res.Body.String())
	}
}

// Credentials must be read from whatever env file the template wrote. The MinIO
// template writes .env (not .env.flatrun), so a deployment with only a .env must
// still resolve credentials (getting past resolution to the reachability step,
// 502, rather than failing at 400 for missing env).
func TestProvisionManagedObjectStore_ReadsPlainEnvFile(t *testing.T) {
	server, router, cleanup := setupObjectStoreTestServer(t)
	defer cleanup()
	token := objStoreLogin(t, router)

	deployment := "minio-store"
	deployDir := filepath.Join(server.config.DeploymentsPath, deployment)
	if err := os.MkdirAll(deployDir, 0o755); err != nil {
		t.Fatalf("mkdir deployment: %v", err)
	}
	env := "MINIO_ROOT_USER=flatrun\nMINIO_ROOT_PASSWORD=generated-secret\n"
	if err := os.WriteFile(filepath.Join(deployDir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}

	res := osReq(t, router, http.MethodPost, "/api/object-stores/provision-managed", token, map[string]any{
		"deployment":     deployment,
		"access_key_env": "MINIO_ROOT_USER",
		"secret_key_env": "MINIO_ROOT_PASSWORD",
		"api_port":       9000,
	})
	if res.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 (creds resolved from .env, store unreachable), got %d %s", res.Code, res.Body.String())
	}
}

// A store whose credentials resolve but that is not reachable (no docker in the
// unit environment) must fail before any credential or destination is created,
// so a failed provision leaves no orphaned state behind. This holds for the
// template path (credentials read from named env vars).
func TestProvisionManagedObjectStore_UnreachableLeavesNoState(t *testing.T) {
	server, router, cleanup := setupObjectStoreTestServer(t)
	defer cleanup()
	token := objStoreLogin(t, router)

	deployment := "my-store"
	deployDir := filepath.Join(server.config.DeploymentsPath, deployment)
	if err := os.MkdirAll(deployDir, 0o755); err != nil {
		t.Fatalf("mkdir deployment: %v", err)
	}
	env := "MINIO_ROOT_USER=flatrun\nMINIO_ROOT_PASSWORD=generated-secret\n"
	if err := os.WriteFile(filepath.Join(deployDir, ".env.flatrun"), []byte(env), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}

	res := osReq(t, router, http.MethodPost, "/api/object-stores/provision-managed", token, map[string]any{
		"deployment":     deployment,
		"access_key_env": "MINIO_ROOT_USER",
		"secret_key_env": "MINIO_ROOT_PASSWORD",
		"api_port":       9000,
	})
	if res.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 when the store is unreachable, got %d %s", res.Code, res.Body.String())
	}
	if creds := server.credentialsManager.ListGenericCredentials(models.CredentialKindS3); len(creds) != 0 {
		t.Fatalf("failed provision left a credential behind: %#v", creds)
	}
	if len(server.config.Backup.Destinations) != 0 {
		t.Fatalf("failed provision left a destination behind: %#v", server.config.Backup.Destinations)
	}
}

func TestListStoreObjects_UnknownStore404(t *testing.T) {
	_, router, cleanup := setupObjectStoreTestServer(t)
	defer cleanup()
	token := objStoreLogin(t, router)

	res := osReq(t, router, http.MethodGet, "/api/object-stores/nope/objects", token, nil)
	if res.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown store, got %d %s", res.Code, res.Body.String())
	}
}

func TestBackupDestinations_ListReportsKind(t *testing.T) {
	server, router, cleanup := setupObjectStoreTestServer(t)
	defer cleanup()
	token := objStoreLogin(t, router)

	server.config.Backup.Destinations = []config.BackupDestination{
		{Name: "s3-prod", Type: "s3", Kind: "external", Bucket: "flatrun-backups", CredentialID: "abc"},
	}

	list := osReq(t, router, http.MethodGet, "/api/backup-destinations", token, nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}
	var resp struct {
		Destinations []config.BackupDestination `json:"destinations"`
	}
	_ = json.Unmarshal(list.Body.Bytes(), &resp)
	if len(resp.Destinations) != 1 || resp.Destinations[0].Bucket != "flatrun-backups" || resp.Destinations[0].Kind != "external" {
		t.Fatalf("unexpected destinations: %#v", resp.Destinations)
	}
}
