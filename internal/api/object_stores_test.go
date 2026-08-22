package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	server.authMiddleware = mw
	api := router.Group("/api")
	api.POST("/auth/login", mw.Login)

	protected := api.Group("")
	protected.Use(mw.RequireAuth())
	protected.GET("/storage-credentials", mw.RequirePermission(auth.PermStorageRead), server.listStorageCredentials)
	protected.POST("/storage-credentials", mw.RequirePermission(auth.PermStorageWrite), server.createStorageCredential)
	protected.PUT("/storage-credentials/:id", mw.RequirePermission(auth.PermStorageWrite), server.updateStorageCredential)
	protected.DELETE("/storage-credentials/:id", mw.RequirePermission(auth.PermStorageDelete), server.deleteStorageCredential)
	protected.GET("/backup-destinations", mw.RequirePermission(auth.PermBackupsRead), server.listBackupDestinations)
	protected.GET("/object-stores", mw.RequirePermission(auth.PermStorageRead), server.listBackupDestinations)
	protected.POST("/object-stores/provision-managed", mw.RequirePermission(auth.PermStorageWrite), server.provisionManagedObjectStore)
	protected.GET("/object-stores/:name/objects", mw.RequirePermission(auth.PermStorageRead), server.listStoreObjects)
	protected.POST("/object-stores/:name/objects", mw.RequirePermission(auth.PermStorageWrite), server.uploadStoreObject)
	protected.GET("/object-stores/:name/objects/download", mw.RequirePermission(auth.PermStorageRead), server.downloadStoreObject)
	protected.DELETE("/object-stores/:name/objects", mw.RequirePermission(auth.PermStorageDelete), server.deleteStoreObject)
	protected.POST("/object-stores/:name/attach", mw.RequirePermission(auth.PermStorageWrite, auth.PermDeploymentsWrite), server.attachStoreToDeployment)
	dnsGroup := protected.Group("/dns")
	dnsGroup.Use(mw.RequirePermission(auth.PermDNSRead), server.requireDNSWriteForMutations())
	dnsGroup.POST("/provider/zones", func(c *gin.Context) { c.Status(http.StatusOK) })
	dnsGroup.POST("/provider/zones/:zone/records/create", func(c *gin.Context) { c.Status(http.StatusOK) })
	firewallGroup := protected.Group("")
	firewallGroup.Use(mw.RequirePermission(auth.PermSecurityRead), server.requireWriteForMethods(auth.PermSecurityWrite, http.MethodPut))
	firewallGroup.GET("/firewall", func(c *gin.Context) { c.Status(http.StatusOK) })
	firewallGroup.PUT("/firewall", func(c *gin.Context) { c.Status(http.StatusOK) })

	cleanup := func() {
		authManager.Close()
		os.RemoveAll(tmpDir)
		os.Unsetenv("FLATRUN_ADMIN_PASSWORD")
	}
	return server, router, cleanup
}

func objectStoreKey(t *testing.T, server *Server, raw string, permissions []string, deployments auth.DeploymentAccess) string {
	t.Helper()
	user, err := server.authManager.CreateUser(raw, "", "password", auth.RoleService, nil)
	if err != nil {
		t.Fatal(err)
	}
	for deployment, level := range deployments {
		if err := server.authManager.AssignDeployment(user.ID, deployment, level, user.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := server.authManager.CreateAPIKeyFromRaw(raw, user.ID, raw, "", auth.Role(""), permissions, deployments, time.Time{}); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestObjectStorePermissionsAreIndependentFromBackups(t *testing.T) {
	server, router, cleanup := setupObjectStoreTestServer(t)
	defer cleanup()

	backupKey := objectStoreKey(t, server, "backup-reader-key", []string{auth.PermBackupsRead.String()}, nil)
	if res := osReq(t, router, http.MethodGet, "/api/object-stores", backupKey, nil); res.Code != http.StatusForbidden {
		t.Fatalf("backup read status = %d, body = %s", res.Code, res.Body.String())
	}

	storageKey := objectStoreKey(t, server, "storage-reader-key", []string{auth.PermStorageRead.String()}, nil)
	if res := osReq(t, router, http.MethodGet, "/api/object-stores", storageKey, nil); res.Code != http.StatusOK {
		t.Fatalf("storage read status = %d, body = %s", res.Code, res.Body.String())
	}
	if res := osReq(t, router, http.MethodPost, "/api/object-stores/provision-managed", storageKey, map[string]string{}); res.Code != http.StatusForbidden {
		t.Fatalf("storage write status = %d, body = %s", res.Code, res.Body.String())
	}
}

func TestServiceReadersCannotMutateDNSOrFirewall(t *testing.T) {
	server, router, cleanup := setupObjectStoreTestServer(t)
	defer cleanup()

	dnsKey := objectStoreKey(t, server, "dns-reader-key", []string{auth.PermDNSRead.String()}, nil)
	if res := osReq(t, router, http.MethodPost, "/api/dns/provider/zones", dnsKey, nil); res.Code != http.StatusOK {
		t.Fatalf("DNS list status = %d, body = %s", res.Code, res.Body.String())
	}
	if res := osReq(t, router, http.MethodPost, "/api/dns/provider/zones/example/records/create", dnsKey, nil); res.Code != http.StatusForbidden {
		t.Fatalf("DNS create status = %d, body = %s", res.Code, res.Body.String())
	}

	firewallKey := objectStoreKey(t, server, "firewall-reader-key", []string{auth.PermSecurityRead.String()}, nil)
	if res := osReq(t, router, http.MethodGet, "/api/firewall", firewallKey, nil); res.Code != http.StatusOK {
		t.Fatalf("firewall read status = %d, body = %s", res.Code, res.Body.String())
	}
	if res := osReq(t, router, http.MethodPut, "/api/firewall", firewallKey, nil); res.Code != http.StatusForbidden {
		t.Fatalf("firewall write status = %d, body = %s", res.Code, res.Body.String())
	}
}

func TestObjectStoreAttachRequiresDeploymentAccess(t *testing.T) {
	server, router, cleanup := setupObjectStoreTestServer(t)
	defer cleanup()
	server.config.Backup.Destinations = []config.BackupDestination{{Name: "store", Type: "s3"}}
	key := objectStoreKey(t, server, "storage-attacher-key", []string{
		auth.PermStorageWrite.String(), auth.PermDeploymentsWrite.String(),
	}, auth.DeploymentAccess{"allowed": auth.AccessLevelWrite})

	res := osReq(t, router, http.MethodPost, "/api/object-stores/store/attach", key, map[string]string{"deployment": "blocked"})
	if res.Code != http.StatusForbidden {
		t.Fatalf("attach status = %d, body = %s", res.Code, res.Body.String())
	}
}

func TestObjectStoreAttachRejectsDeploymentPath(t *testing.T) {
	server, router, cleanup := setupObjectStoreTestServer(t)
	defer cleanup()
	server.config.Backup.Destinations = []config.BackupDestination{{Name: "store", Type: "s3"}}

	res := osReq(t, router, http.MethodPost, "/api/object-stores/store/attach", objStoreLogin(t, router), map[string]string{"deployment": "../outside"})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("attach status = %d, body = %s", res.Code, res.Body.String())
	}
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

func TestAttachStoreToDeployment_WritesEnvAndWiresCompose(t *testing.T) {
	server, router, cleanup := setupObjectStoreTestServer(t)
	defer cleanup()
	token := objStoreLogin(t, router)

	cred, err := server.credentialsManager.CreateGenericCredential("app-keys", models.CredentialKindS3, map[string]string{
		"access_key_id": "AKIATEST", "secret_access_key": "shh",
	})
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	server.config.Backup.Destinations = []config.BackupDestination{{
		Name: "r2", Type: "s3", Kind: "external", Endpoint: "https://s3.example.com",
		Region: "us-east-1", Bucket: "assets", CredentialID: cred.ID, UsePathStyle: true,
	}}

	app := "webapp"
	appDir := filepath.Join(server.config.DeploymentsPath, app)
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	compose := "services:\n  app:\n    image: node:20-alpine\n"
	if err := os.WriteFile(filepath.Join(appDir, "docker-compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}

	res := osReq(t, router, http.MethodPost, "/api/object-stores/r2/attach", token, map[string]any{
		"deployment": app,
	})
	if res.Code != http.StatusOK {
		t.Fatalf("attach: %d %s", res.Code, res.Body.String())
	}

	env, err := os.ReadFile(filepath.Join(appDir, ".env.flatrun"))
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	for _, want := range []string{"S3_ENDPOINT=https://s3.example.com", "S3_BUCKET=assets", "S3_ACCESS_KEY_ID=AKIATEST", "S3_SECRET_ACCESS_KEY=shh"} {
		if !strings.Contains(string(env), want) {
			t.Fatalf("env missing %q:\n%s", want, env)
		}
	}

	updated, _ := os.ReadFile(filepath.Join(appDir, "docker-compose.yml"))
	if !strings.Contains(string(updated), "env_file") || !strings.Contains(string(updated), ".env.flatrun") {
		t.Fatalf("compose not wired to env file:\n%s", updated)
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
