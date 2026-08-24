package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/backup"
	"github.com/flatrun/agent/internal/contextkeys"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/nginx"
	"github.com/flatrun/agent/internal/proxy"
	"github.com/flatrun/agent/internal/scheduler"
	"github.com/flatrun/agent/internal/security"
	"github.com/flatrun/agent/internal/traffic"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

func testActor(role auth.Role, deployments map[string]string) *auth.ActorContext {
	return &auth.ActorContext{
		Type:        "user",
		Role:        role,
		Deployments: deployments,
	}
}

func actorMiddleware(actor *auth.ActorContext) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(contextkeys.Actor, actor)
		c.Next()
	}
}

func TestClusterServiceCredentialsRejectUnscopedSensitiveResources(t *testing.T) {
	gin.SetMode(gin.TestMode)
	actor := &auth.ActorContext{
		Type: "api_key",
		Role: auth.RoleService,
		User: &auth.User{Role: auth.RoleService, Username: "__flatrun_cluster"},
	}

	for _, path := range []string{"/api/backups/other", "/api/credentials", "/api/security/events"} {
		router := gin.New()
		router.Use(actorMiddleware(actor), restrictClusterServiceResources)
		router.GET(path, func(c *gin.Context) { c.Status(http.StatusNoContent) })
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s status = %d", path, response.Code)
		}
	}

	router := gin.New()
	router.Use(actorMiddleware(actor), restrictClusterServiceResources)
	router.GET("/api/deployments/:name/security", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	request := httptest.NewRequest(http.MethodGet, "/api/deployments/app/security", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("deployment-scoped status = %d", response.Code)
	}
}

func TestClusterServiceDeploymentHeaderNarrowsResourceAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	actor := &auth.ActorContext{
		Type: "api_key",
		Role: auth.RoleService,
		User: &auth.User{Role: auth.RoleService, Username: "__flatrun_cluster"},
		APIKey: &auth.APIKey{
			Deployments: auth.DeploymentAccess{"allowed": auth.AccessLevelAdmin, "other": auth.AccessLevelAdmin},
		},
	}

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		router := gin.New()
		router.Use(actorMiddleware(actor), restrictClusterServiceResources)
		router.Handle(method, "/api/resources/:deployment", func(c *gin.Context) {
			scoped := auth.GetActorFromContext(c)
			if !scoped.CanAccessDeployment(c.Param("deployment"), auth.AccessLevelRead) {
				c.Status(http.StatusForbidden)
				return
			}
			c.Status(http.StatusNoContent)
		})

		allowed := httptest.NewRequest(method, "/api/resources/allowed", nil)
		allowed.Header.Set("X-FlatRun-Deployment", "allowed")
		allowedResponse := httptest.NewRecorder()
		router.ServeHTTP(allowedResponse, allowed)
		if allowedResponse.Code != http.StatusNoContent {
			t.Fatalf("%s allowed status = %d", method, allowedResponse.Code)
		}

		other := httptest.NewRequest(method, "/api/resources/other", nil)
		other.Header.Set("X-FlatRun-Deployment", "allowed")
		otherResponse := httptest.NewRecorder()
		router.ServeHTTP(otherResponse, other)
		if otherResponse.Code != http.StatusForbidden {
			t.Fatalf("%s cross-deployment status = %d", method, otherResponse.Code)
		}
	}
}

func TestClusterServiceDeploymentHeaderCannotWidenPeerPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	actor := &auth.ActorContext{
		Type: "api_key",
		Role: auth.RoleService,
		User: &auth.User{Role: auth.RoleService, Username: "__flatrun_cluster"},
		APIKey: &auth.APIKey{
			Deployments: auth.DeploymentAccess{"allowed": auth.AccessLevelRead},
		},
	}

	router := gin.New()
	router.Use(actorMiddleware(actor), restrictClusterServiceResources)
	router.GET("/api/resources", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	request := httptest.NewRequest(http.MethodGet, "/api/resources", nil)
	request.Header.Set("X-FlatRun-Deployment", "other")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestListVirtualHostsFiltersByDeploymentAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tmpDir, err := os.MkdirTemp("", "vhosts-authz-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	confDir := filepath.Join(tmpDir, "nginx", "conf.d")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		t.Fatalf("failed to create conf dir: %v", err)
	}
	for _, name := range []string{"allowed-app.conf", "other-app.conf"} {
		if err := os.WriteFile(filepath.Join(confDir, name), []byte("server {}"), 0644); err != nil {
			t.Fatalf("failed to write vhost: %v", err)
		}
	}

	cfg := &config.Config{DeploymentsPath: tmpDir, Nginx: config.NginxConfig{ConfigPath: confDir}}
	server := &Server{
		config: cfg,
		proxyOrchestrator: proxy.NewOrchestratorWithManagers(
			nginx.NewManager(&cfg.Nginx, tmpDir, ""),
			nil,
		),
	}

	router := gin.New()
	router.Use(actorMiddleware(testActor(auth.RoleOperator, map[string]string{"allowed-app": auth.AccessLevelRead})))
	router.GET("/proxy/vhosts", server.listVirtualHosts)

	req := httptest.NewRequest(http.MethodGet, "/proxy/vhosts", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		VirtualHosts []struct {
			Name string `json:"name"`
		} `json:"virtual_hosts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.VirtualHosts) != 1 || resp.VirtualHosts[0].Name != "allowed-app" {
		t.Fatalf("expected only allowed-app vhost, got %#v", resp.VirtualHosts)
	}
}

func TestSyncAllProxiesRequiresAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := &Server{}
	router := gin.New()
	router.Use(actorMiddleware(testActor(auth.RoleOperator, map[string]string{"app": auth.AccessLevelWrite})))
	router.POST("/proxy/sync", server.syncAllProxies)

	req := httptest.NewRequest(http.MethodPost, "/proxy/sync", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSecurityEventsRequireDeploymentFilterForNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tmpDir, err := os.MkdirTemp("", "security-authz-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	securityManager, err := security.NewManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create security manager: %v", err)
	}
	defer securityManager.Close()

	server := &Server{securityManager: securityManager}
	router := gin.New()
	router.Use(actorMiddleware(testActor(auth.RoleOperator, map[string]string{"app": auth.AccessLevelRead})))
	router.GET("/security/events", server.listSecurityEvents)

	req := httptest.NewRequest(http.MethodGet, "/security/events", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestTrafficLogsKeepDBTotalForScopedNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tmpDir, err := os.MkdirTemp("", "traffic-authz-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	trafficManager, err := traffic.NewManager(tmpDir, 7)
	if err != nil {
		t.Fatalf("failed to create traffic manager: %v", err)
	}
	defer trafficManager.Close()

	for i := 0; i < 2; i++ {
		if _, err := trafficManager.IngestLog(&traffic.IngestTrafficLog{
			DeploymentName: "app",
			RequestPath:    "/",
			RequestMethod:  "GET",
			StatusCode:     200,
			SourceIP:       "203.0.113.10",
		}); err != nil {
			t.Fatalf("failed to ingest traffic log: %v", err)
		}
	}

	server := &Server{trafficManager: trafficManager}
	router := gin.New()
	router.Use(actorMiddleware(testActor(auth.RoleOperator, map[string]string{"app": auth.AccessLevelRead})))
	router.GET("/traffic/logs", server.getTrafficLogs)

	req := httptest.NewRequest(http.MethodGet, "/traffic/logs?deployment=app&limit=1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Total int                  `json:"total"`
		Logs  []traffic.TrafficLog `json:"logs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.Logs) != 1 {
		t.Fatalf("expected one paged log, got %d", len(resp.Logs))
	}
	if resp.Total != 2 {
		t.Fatalf("expected DB total 2, got %d", resp.Total)
	}
}

func TestUpdateContainerResourcesValidatesBodyBeforeContainerLookup(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := &Server{}
	router := gin.New()
	router.Use(actorMiddleware(testActor(auth.RoleAdmin, nil)))
	router.PUT("/containers/:id/resources", server.updateContainerResources)

	req := httptest.NewRequest(http.MethodPut, "/containers/does-not-exist/resources", bytes.NewBufferString("{invalid"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 before container lookup, got %d: %s", w.Code, w.Body.String())
	}
}

func TestContainerLifecycleDeniesUnscopedContainerForNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := &Server{}
	router := gin.New()
	router.Use(actorMiddleware(testActor(auth.RoleOperator, map[string]string{"app": auth.AccessLevelWrite})))
	router.POST("/containers/:id/start", server.startContainer)
	router.POST("/containers/:id/stop", server.stopContainer)
	router.POST("/containers/:id/restart", server.restartContainer)
	router.DELETE("/containers/:id", server.removeContainer)

	tests := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/containers/does-not-exist/start"},
		{http.MethodPost, "/containers/does-not-exist/stop"},
		{http.MethodPost, "/containers/does-not-exist/restart"},
		{http.MethodDelete, "/containers/does-not-exist"},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s: expected 403, got %d: %s", tt.method, tt.path, w.Code, w.Body.String())
		}
	}
}

func TestRestoreBackupRequiresWriteOnTargetDeployment(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tmpDir, err := os.MkdirTemp("", "backup-authz-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	for _, name := range []string{"source-app", "target-app"} {
		createTestDeployment(t, tmpDir, name, &models.ServiceMetadata{Name: name})
	}

	backupManager, err := backup.NewManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create backup manager: %v", err)
	}
	created, err := backupManager.CreateBackup(context.Background(), "source-app", nil)
	if err != nil {
		t.Fatalf("failed to create backup: %v", err)
	}

	server := &Server{backupManager: backupManager}
	router := gin.New()
	router.Use(actorMiddleware(testActor(auth.RoleOperator, map[string]string{
		"source-app": auth.AccessLevelRead,
		"target-app": auth.AccessLevelRead,
	})))
	router.POST("/backups/:id/restore", server.restoreBackup)

	body := bytes.NewBufferString(`{"deployment_name":"target-app"}`)
	req := httptest.NewRequest(http.MethodPost, "/backups/"+created.ID+"/restore", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateScheduledTaskRequiresWriteDeploymentAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tmpDir, err := os.MkdirTemp("", "scheduler-authz-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	createTestDeployment(t, tmpDir, "app", &models.ServiceMetadata{Name: "app"})

	backupManager, err := backup.NewManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create backup manager: %v", err)
	}
	manager := docker.NewManager(tmpDir)
	schedulerManager, err := scheduler.NewManager(tmpDir, scheduler.NewExecutor(backupManager, manager))
	if err != nil {
		t.Fatalf("failed to create scheduler manager: %v", err)
	}
	defer schedulerManager.Stop()

	server := &Server{manager: manager, schedulerManager: schedulerManager}
	router := gin.New()
	router.Use(actorMiddleware(testActor(auth.RoleOperator, map[string]string{"app": auth.AccessLevelRead})))
	router.POST("/scheduler/tasks", server.createScheduledTask)

	body := bytes.NewBufferString(`{"name":"backup","type":"backup","deployment_name":"app","cron_expr":"0 * * * *","enabled":true}`)
	req := httptest.NewRequest(http.MethodPost, "/scheduler/tasks", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}
