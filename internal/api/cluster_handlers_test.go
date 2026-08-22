package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/capacity"
	"github.com/flatrun/agent/internal/cluster"
	"github.com/flatrun/agent/internal/orchestrator"
	"github.com/flatrun/agent/internal/routing"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
)

func TestCapacityClaimUsesPeerSpecificGrant(t *testing.T) {
	peer, err := clusterPeerFromActor(&auth.ActorContext{APIKey: &auth.APIKey{Name: "cluster-peer-prod1"}})
	if err != nil || peer != "prod1" {
		t.Fatalf("peer = %q, error = %v", peer, err)
	}
	grant, ok := capacityOfferGrant(cluster.PeerPolicy{Grants: []cluster.Grant{
		{Capability: cluster.CapabilityCapacityRead},
		{Capability: cluster.CapabilityCapacityOffer, MaxCPU: 2, MaxMemory: 4 << 30, MaxReplicas: 3},
	}})
	if !ok || grant.MaxReplicas != 3 || grant.MaxCPU != 2 {
		t.Fatalf("grant = %#v, found = %v", grant, ok)
	}
	if capacityNodeLabel("prod1") == capacityNodeLabel("prod2") {
		t.Fatal("peer labels must be isolated")
	}
}

type testClusterEnv struct {
	server  *Server
	router  *gin.Engine
	tmpDir  string
	cleanup func()
}

func setupClusterTestServer(t *testing.T, serverName string, clusterEnabled bool) *testClusterEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	tmpDir, err := os.MkdirTemp("", "cluster_api_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	cfg := &config.Config{
		DeploymentsPath: tmpDir,
		API: config.APIConfig{
			Host: "127.0.0.1",
			Port: 8090,
		},
		Auth: config.AuthConfig{
			Enabled:   true,
			JWTSecret: "test-jwt-secret-for-cluster",
			APIKeys:   []string{"legacy-test-key"},
		},
		Cluster: config.ClusterConfig{
			Enabled:        clusterEnabled,
			ServerName:     serverName,
			HealthInterval: "30s",
			RequestTimeout: "5s",
		},
	}

	os.Setenv("FLATRUN_ADMIN_PASSWORD", "testadminpass")

	authManager, err := auth.NewManager(tmpDir, &cfg.Auth, true)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create auth manager: %v", err)
	}

	var clusterManager *cluster.Manager
	if clusterEnabled {
		clusterDB, err := cluster.NewDB(tmpDir)
		if err != nil {
			authManager.Close()
			os.RemoveAll(tmpDir)
			t.Fatalf("Failed to create cluster DB: %v", err)
		}
		clusterManager = cluster.NewManager(clusterDB, serverName, 30*time.Second, 5*time.Second, cfg.Auth.JWTSecret)
		if err := clusterManager.Start(context.Background()); err != nil {
			authManager.Close()
			os.RemoveAll(tmpDir)
			t.Fatalf("Failed to start cluster manager: %v", err)
		}
	}

	server := &Server{
		config:         cfg,
		configPath:     tmpDir + "/config.yml",
		authManager:    authManager,
		clusterManager: clusterManager,
	}

	router := gin.New()
	authMiddleware := auth.NewMiddlewareWithManager(&cfg.Auth, authManager)

	api := router.Group("/api")
	api.POST("/auth/login", authMiddleware.Login)

	api.POST("/cluster/exchange", server.clusterExchange)

	protected := api.Group("")
	protected.Use(authMiddleware.RequireAuth())
	{
		protected.GET("/capacity", authMiddleware.RequirePermission(auth.PermSystemRead), server.getCapacityStatus)
		protected.GET("/test/deployments", authMiddleware.RequirePermission(auth.PermDeploymentsRead), func(c *gin.Context) {
			c.Status(http.StatusNoContent)
		})
		protected.GET("/test/users", authMiddleware.RequirePermission(auth.PermUsersWrite), func(c *gin.Context) {
			c.Status(http.StatusNoContent)
		})
		protected.POST("/cluster/capacity/claim", authMiddleware.RequirePermission(auth.PermClusterCapacityClaim), server.clusterCapacityClaim)
		clusterGroup := protected.Group("/cluster")
		clusterGroup.Use(authMiddleware.RequirePermission(auth.PermClusterRead))
		{
			clusterGroup.GET("/status", server.clusterStatus)
			clusterGroup.GET("/providers", server.clusterProviders)
			clusterGroup.PUT("/providers", authMiddleware.RequirePermission(auth.PermClusterWrite), server.updateClusterProviders)
			clusterGroup.POST("/setup", authMiddleware.RequirePermission(auth.PermClusterWrite), server.clusterSetup)
			clusterGroup.GET("/peers", server.clusterListPeers)
			clusterGroup.GET("/peers/:name/policy", server.clusterPeerPolicy)
			clusterGroup.PUT("/peers/:name/policy", authMiddleware.RequirePermission(auth.PermClusterWrite), server.updateClusterPeerPolicy)
			clusterGroup.POST("/invite", authMiddleware.RequirePermission(auth.PermClusterWrite), server.clusterInvite)
			clusterGroup.POST("/accept", authMiddleware.RequirePermission(auth.PermClusterWrite), server.clusterAccept)
			clusterGroup.DELETE("/peers/:name", authMiddleware.RequirePermission(auth.PermClusterWrite), server.clusterRemovePeer)
			clusterGroup.GET("/peers/:name/proxy/*path", server.clusterProxy)
			clusterGroup.POST("/peers/:name/proxy/*path", authMiddleware.RequirePermission(auth.PermClusterWrite), server.clusterProxy)
			clusterGroup.PUT("/peers/:name/proxy/*path", authMiddleware.RequirePermission(auth.PermClusterWrite), server.clusterProxy)
			clusterGroup.PATCH("/peers/:name/proxy/*path", authMiddleware.RequirePermission(auth.PermClusterWrite), server.clusterProxy)
			clusterGroup.DELETE("/peers/:name/proxy/*path", authMiddleware.RequirePermission(auth.PermClusterWrite), server.clusterProxy)
			clusterGroup.GET("/deployments", server.clusterAggregateDeployments)
			clusterGroup.GET("/stats", server.clusterAggregateStats)
			clusterGroup.GET("/capacity", server.clusterAggregateCapacity)
		}
	}

	cleanup := func() {
		if manager := server.getClusterManager(); manager != nil {
			manager.Stop()
		}
		authManager.Close()
		os.RemoveAll(tmpDir)
	}

	return &testClusterEnv{
		server:  server,
		router:  router,
		tmpDir:  tmpDir,
		cleanup: cleanup,
	}
}

func TestClusterCapacityIncludesLocalOfferPolicy(t *testing.T) {
	env := setupClusterTestServer(t, "server-a", true)
	defer env.cleanup()

	token := clusterLogin(t, env.router)
	req := httptest.NewRequest(http.MethodGet, "/api/cluster/capacity", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Servers map[string]struct {
			Online bool `json:"online"`
			Data   struct {
				Offer capacity.Offer `json:"offer"`
			} `json:"data"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	local, ok := response.Servers["server-a"]
	if !ok || !local.Online {
		t.Fatalf("local server = %#v", local)
	}
	if local.Data.Offer.Enabled {
		t.Fatal("fleet capacity should require explicit permission")
	}
}

func TestUpdateClusterPeerPolicyThroughHTTP(t *testing.T) {
	env := setupClusterTestServer(t, "server-a", true)
	defer env.cleanup()
	if err := env.server.clusterManager.AddPeer("server-b", "https://server-b.example.com", "peer-key"); err != nil {
		t.Fatalf("AddPeer failed: %v", err)
	}
	if err := env.server.createClusterAPIKey("server-b-inbound-key", "server-b"); err != nil {
		t.Fatalf("createClusterAPIKey failed: %v", err)
	}

	token := clusterLogin(t, env.router)
	body := []byte(`{"grants":[{"capability":"capacity.offer","max_cpu":2,"max_memory":2147483648,"max_replicas":2}]}`)
	req := httptest.NewRequest(http.MethodPut, "/api/cluster/peers/server-b/policy", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/cluster/peers/server-b/policy", nil)
	getReq.Header.Set("Authorization", "Bearer "+token)
	getWriter := httptest.NewRecorder()
	env.router.ServeHTTP(getWriter, getReq)
	if getWriter.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", getWriter.Code, getWriter.Body.String())
	}
	var policy cluster.PeerPolicy
	if err := json.Unmarshal(getWriter.Body.Bytes(), &policy); err != nil {
		t.Fatalf("decode policy: %v", err)
	}
	if len(policy.Grants) != 1 || policy.Grants[0].MaxCPU != 2 || policy.Grants[0].MaxReplicas != 2 {
		t.Fatalf("policy = %#v", policy)
	}
	keys, err := env.server.authManager.GetAllAPIKeys()
	if err != nil {
		t.Fatalf("list API keys: %v", err)
	}
	for _, key := range keys {
		if key.Name == "cluster-peer-server-b" && !slices.Equal(key.Permissions, []string{auth.PermClusterCapacityClaim.String()}) {
			t.Fatalf("capacity offer permissions = %#v", key.Permissions)
		}
	}
	claimReq := httptest.NewRequest(http.MethodPost, "/api/cluster/capacity/claim", nil)
	claimReq.Header.Set("Authorization", "Bearer server-b-inbound-key")
	claimWriter := httptest.NewRecorder()
	env.router.ServeHTTP(claimWriter, claimReq)
	if claimWriter.Code == http.StatusForbidden || claimWriter.Code == http.StatusUnauthorized {
		t.Fatalf("capacity offer credential was rejected by middleware: %d: %s", claimWriter.Code, claimWriter.Body.String())
	}
}

func TestReconcileClusterPeerPoliciesScopesExistingCredentials(t *testing.T) {
	env := setupClusterTestServer(t, "server-a", true)
	defer env.cleanup()
	if err := env.server.clusterManager.AddPeer("server-b", "https://server-b.example.com", "peer-key"); err != nil {
		t.Fatalf("AddPeer failed: %v", err)
	}
	user, err := env.server.authManager.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("get admin: %v", err)
	}
	_, err = env.server.authManager.CreateAPIKeyFromRaw(
		"existing-peer-key",
		user.ID,
		"cluster-peer-server-b",
		"Existing Fleet peer",
		auth.RoleAdmin,
		nil,
		nil,
		time.Time{},
	)
	if err != nil {
		t.Fatalf("create existing peer credential: %v", err)
	}

	if err := env.server.reconcileClusterPeerPolicies(); err != nil {
		t.Fatalf("reconcile peer policies: %v", err)
	}
	keys, err := env.server.authManager.GetAllAPIKeys()
	if err != nil {
		t.Fatalf("list API keys: %v", err)
	}
	for _, key := range keys {
		if key.Name != "cluster-peer-server-b" {
			continue
		}
		if key.Role != "" {
			t.Fatalf("peer role = %q", key.Role)
		}
		permissions, _ := clusterPolicyAccess(cluster.PeerPolicy{Grants: cluster.DefaultPeerGrants()})
		if !slices.Equal(key.Permissions, permissions) {
			t.Fatalf("peer permissions = %#v, want %#v", key.Permissions, permissions)
		}
		return
	}
	t.Fatal("existing peer credential not found")
}

func TestClusterPolicyAccessScopesDeployments(t *testing.T) {
	permissions, deployments := clusterPolicyAccess(cluster.PeerPolicy{Grants: []cluster.Grant{
		{Capability: cluster.CapabilityDeploymentsRead, Deployments: []string{"public-site", "docs"}},
		{Capability: cluster.CapabilityDeploymentsRun, Deployments: []string{"public-site"}},
		{Capability: cluster.CapabilityCapacityRead},
	}})

	if deployments["public-site"] != auth.AccessLevelWrite || deployments["docs"] != auth.AccessLevelRead {
		t.Fatalf("deployment access = %#v", deployments)
	}
	wanted := map[string]bool{
		auth.PermDeploymentsRead.String():  true,
		auth.PermDeploymentsWrite.String(): true,
		auth.PermContainersRead.String():   true,
		auth.PermContainersWrite.String():  true,
		auth.PermSystemRead.String():       true,
	}
	for _, permission := range permissions {
		delete(wanted, permission)
	}
	if len(wanted) != 0 {
		t.Fatalf("missing permissions = %#v", wanted)
	}
}

func TestClusterSetupEnablesClusterWithoutRestart(t *testing.T) {
	env := setupClusterTestServer(t, "", false)
	defer env.cleanup()

	token := clusterLogin(t, env.router)
	body, _ := json.Marshal(map[string]string{
		"server_name":   "server-a",
		"advertise_url": "https://server-a.example.com/",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cluster/setup", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/api/cluster/status", nil)
	statusReq.Header.Set("Authorization", "Bearer "+token)
	statusWriter := httptest.NewRecorder()
	env.router.ServeHTTP(statusWriter, statusReq)
	if statusWriter.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", statusWriter.Code, statusWriter.Body.String())
	}

	var status map[string]interface{}
	if err := json.Unmarshal(statusWriter.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status["enabled"] != true || status["server_name"] != "server-a" {
		t.Fatalf("Unexpected status: %s", statusWriter.Body.String())
	}
	if env.server.config.Cluster.AdvertiseURL != "https://server-a.example.com" {
		t.Fatalf("AdvertiseURL = %q", env.server.config.Cluster.AdvertiseURL)
	}
}

func TestClusterSetupRejectsInvalidAdvertiseURL(t *testing.T) {
	env := setupClusterTestServer(t, "", false)
	defer env.cleanup()

	token := clusterLogin(t, env.router)
	body, _ := json.Marshal(map[string]string{"server_name": "server-a", "advertise_url": "server-a"})
	req := httptest.NewRequest(http.MethodPost, "/api/cluster/setup", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("Expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if env.server.getClusterManager() != nil {
		t.Fatal("Cluster manager started after invalid setup")
	}
}

func TestClusterAPIKeyUsesExplicitPermissions(t *testing.T) {
	env := setupClusterTestServer(t, "server-a", true)
	defer env.cleanup()

	if err := env.server.createClusterAPIKey("peer-key-for-test", "server-b"); err != nil {
		t.Fatalf("createClusterAPIKey failed: %v", err)
	}
	keys, err := env.server.authManager.GetAllAPIKeys()
	if err != nil {
		t.Fatal(err)
	}
	var peerKey *auth.APIKey
	for i := range keys {
		if keys[i].Name == "cluster-peer-server-b" {
			peerKey = &keys[i]
			break
		}
	}
	if peerKey == nil {
		t.Fatal("Cluster API key was not created")
	}
	if peerKey.Role == auth.RoleAdmin {
		t.Fatal("Cluster API key has administrator role")
	}
	owner, err := env.server.authManager.GetUser(peerKey.UserID)
	if err != nil {
		t.Fatalf("get key owner: %v", err)
	}
	if owner.Role != auth.RoleService {
		t.Fatalf("key owner role = %q", owner.Role)
	}
	actor, err := env.server.authManager.BuildActorContext(owner, peerKey)
	if err != nil {
		t.Fatalf("build actor: %v", err)
	}
	if actor.HasPermission(auth.PermUsersWrite) || !actor.HasPermission(auth.PermDeploymentsWrite) {
		t.Fatalf("effective actor = %#v", actor)
	}
	permissions := make(map[string]bool, len(peerKey.Permissions))
	for _, permission := range peerKey.Permissions {
		permissions[permission] = true
	}
	if !permissions[auth.PermDeploymentsRead.String()] || !permissions[auth.PermDeploymentsWrite.String()] {
		t.Fatalf("permissions = %#v", peerKey.Permissions)
	}
	if permissions[auth.PermUsersWrite.String()] || permissions[auth.PermConfigWrite.String()] {
		t.Fatalf("permissions include administrative access: %#v", peerKey.Permissions)
	}
}

func TestClusterAPIKeyEnforcesPermissionsThroughHTTP(t *testing.T) {
	env := setupClusterTestServer(t, "server-a", true)
	defer env.cleanup()
	const rawKey = "peer-http-key-for-test"
	if err := env.server.createClusterAPIKey(rawKey, "server-b"); err != nil {
		t.Fatalf("createClusterAPIKey failed: %v", err)
	}

	request := func(path string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+rawKey)
		w := httptest.NewRecorder()
		env.router.ServeHTTP(w, req)
		return w.Code
	}
	if status := request("/api/test/deployments"); status != http.StatusNoContent {
		t.Fatalf("deployment read status = %d", status)
	}
	if status := request("/api/test/users"); status != http.StatusForbidden {
		t.Fatalf("user write status = %d", status)
	}
}

func TestCapacityClaimRejectsUnpermittedPeerThroughHTTP(t *testing.T) {
	env := setupClusterTestServer(t, "server-a", true)
	defer env.cleanup()
	const rawKey = "peer-capacity-key-for-test"
	if err := env.server.getClusterManager().AddPeer("server-b", "http://server-b.invalid", "remote-key"); err != nil {
		t.Fatal(err)
	}
	if err := env.server.createClusterAPIKey(rawKey, "server-b"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/cluster/capacity/claim", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
}

func clusterLogin(t *testing.T, router *gin.Engine) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "testadminpass",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Login failed: %d - %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return resp["token"].(string)
}

func TestClusterStatusDisabled(t *testing.T) {
	env := setupClusterTestServer(t, "disabled-server", false)
	defer env.cleanup()

	token := clusterLogin(t, env.router)

	req := httptest.NewRequest(http.MethodGet, "/api/cluster/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["enabled"] != false {
		t.Error("Expected enabled=false when cluster is disabled")
	}
}

func TestClusterStatusEnabled(t *testing.T) {
	env := setupClusterTestServer(t, "my-server", true)
	defer env.cleanup()

	token := clusterLogin(t, env.router)

	req := httptest.NewRequest(http.MethodGet, "/api/cluster/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["enabled"] != true {
		t.Error("Expected enabled=true")
	}
	if resp["server_name"] != "my-server" {
		t.Errorf("server_name = %v, want my-server", resp["server_name"])
	}
	if resp["peer_count"] != float64(0) {
		t.Errorf("peer_count = %v, want 0", resp["peer_count"])
	}
}

func TestClusterListPeersEmpty(t *testing.T) {
	env := setupClusterTestServer(t, "server-a", true)
	defer env.cleanup()

	token := clusterLogin(t, env.router)

	req := httptest.NewRequest(http.MethodGet, "/api/cluster/peers", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	peers := resp["peers"]
	if peers != nil {
		peerList, ok := peers.([]interface{})
		if ok && len(peerList) != 0 {
			t.Errorf("Expected empty peers list, got %d peers", len(peerList))
		}
	}
}

func TestClusterListPeersDisabled(t *testing.T) {
	env := setupClusterTestServer(t, "disabled", false)
	defer env.cleanup()

	token := clusterLogin(t, env.router)

	req := httptest.NewRequest(http.MethodGet, "/api/cluster/peers", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 when cluster disabled, got %d", w.Code)
	}
}

func TestClusterInviteAndExchange(t *testing.T) {
	env := setupClusterTestServer(t, "server-a", true)
	defer env.cleanup()

	token := clusterLogin(t, env.router)

	// Step 1: Create invite
	req := httptest.NewRequest(http.MethodPost, "/api/cluster/invite", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Invite failed: %d - %s", w.Code, w.Body.String())
	}

	var inviteResp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &inviteResp)

	inviteToken, ok := inviteResp["invite_token"].(string)
	if !ok || inviteToken == "" {
		t.Fatal("Expected non-empty invite_token in response")
	}

	if _, ok := inviteResp["expires_at"]; !ok {
		t.Error("Expected expires_at in response")
	}

	// Step 2: Exchange (simulate Server B calling exchange on Server A)
	exchangeBody, _ := json.Marshal(map[string]string{
		"invite_token": inviteToken,
		"url":          "https://server-b.example.com:8090",
		"api_key":      "server-b-api-key-for-a",
		"name":         "server-b",
	})

	req = httptest.NewRequest(http.MethodPost, "/api/cluster/exchange", bytes.NewBuffer(exchangeBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Exchange failed: %d - %s", w.Code, w.Body.String())
	}

	var exchangeResp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &exchangeResp)

	if exchangeResp["name"] != "server-a" {
		t.Errorf("Exchange name = %v, want server-a", exchangeResp["name"])
	}
	apiKey, ok := exchangeResp["api_key"].(string)
	if !ok || apiKey == "" {
		t.Error("Expected non-empty api_key in exchange response")
	}

	// Step 3: Verify peer was added
	req = httptest.NewRequest(http.MethodGet, "/api/cluster/peers", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	var peersResp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &peersResp)

	peers, ok := peersResp["peers"].([]interface{})
	if !ok || len(peers) != 1 {
		t.Fatalf("Expected 1 peer after exchange, got %v", peersResp["peers"])
	}

	peer := peers[0].(map[string]interface{})
	if peer["name"] != "server-b" {
		t.Errorf("Peer name = %v, want server-b", peer["name"])
	}
}

func TestClusterExchangeInvalidToken(t *testing.T) {
	env := setupClusterTestServer(t, "server-a", true)
	defer env.cleanup()

	exchangeBody, _ := json.Marshal(map[string]string{
		"invite_token": "invalid-token",
		"url":          "https://b.example.com",
		"api_key":      "key",
		"name":         "server-b",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/cluster/exchange", bytes.NewBuffer(exchangeBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected 404 for invalid token, got %d: %s", w.Code, w.Body.String())
	}
}

func TestClusterExchangeTokenReuse(t *testing.T) {
	env := setupClusterTestServer(t, "server-a", true)
	defer env.cleanup()

	token := clusterLogin(t, env.router)

	// Create invite
	req := httptest.NewRequest(http.MethodPost, "/api/cluster/invite", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	var inviteResp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &inviteResp)
	inviteToken := inviteResp["invite_token"].(string)

	// First exchange succeeds
	exchangeBody, _ := json.Marshal(map[string]string{
		"invite_token": inviteToken,
		"url":          "https://b.example.com",
		"api_key":      "key-b",
		"name":         "server-b",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/cluster/exchange", bytes.NewBuffer(exchangeBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("First exchange should succeed: %d", w.Code)
	}

	// Second exchange with same token fails
	exchangeBody, _ = json.Marshal(map[string]string{
		"invite_token": inviteToken,
		"url":          "https://c.example.com",
		"api_key":      "key-c",
		"name":         "server-c",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/cluster/exchange", bytes.NewBuffer(exchangeBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("Reused token should return 409 Conflict, got %d: %s", w.Code, w.Body.String())
	}
}

func TestClusterRemovePeer(t *testing.T) {
	env := setupClusterTestServer(t, "server-a", true)
	defer env.cleanup()

	token := clusterLogin(t, env.router)

	// Add a peer via exchange
	invReq := httptest.NewRequest(http.MethodPost, "/api/cluster/invite", nil)
	invReq.Header.Set("Authorization", "Bearer "+token)
	invW := httptest.NewRecorder()
	env.router.ServeHTTP(invW, invReq)

	var invResp map[string]interface{}
	json.Unmarshal(invW.Body.Bytes(), &invResp)

	exchangeBody, _ := json.Marshal(map[string]string{
		"invite_token": invResp["invite_token"].(string),
		"url":          "https://b.example.com",
		"api_key":      "key-b",
		"name":         "server-b",
	})
	exReq := httptest.NewRequest(http.MethodPost, "/api/cluster/exchange", bytes.NewBuffer(exchangeBody))
	exReq.Header.Set("Content-Type", "application/json")
	exW := httptest.NewRecorder()
	env.router.ServeHTTP(exW, exReq)

	// Remove peer
	req := httptest.NewRequest(http.MethodDelete, "/api/cluster/peers/server-b", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify removed
	req = httptest.NewRequest(http.MethodGet, "/api/cluster/peers", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	var peersResp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &peersResp)

	if peersResp["peers"] != nil {
		peerList, ok := peersResp["peers"].([]interface{})
		if ok && len(peerList) != 0 {
			t.Errorf("Expected 0 peers after removal, got %d", len(peerList))
		}
	}
}

func TestClusterProxyForwardsToPeer(t *testing.T) {
	peerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"proxied_path":   r.URL.Path,
			"proxied_method": r.Method,
		})
	}))
	defer peerServer.Close()

	env := setupClusterTestServer(t, "primary", true)
	defer env.cleanup()

	_ = env.server.clusterManager.AddPeer("remote", peerServer.URL, "key")

	token := clusterLogin(t, env.router)

	req := httptest.NewRequest(http.MethodGet, "/api/cluster/peers/remote/proxy/deployments", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Proxy failed: %d - %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["proxied_path"] != "/api/deployments" {
		t.Errorf("Proxied path = %s, want /api/deployments", resp["proxied_path"])
	}
	if resp["proxied_method"] != "GET" {
		t.Errorf("Proxied method = %s, want GET", resp["proxied_method"])
	}
}

func TestClusterProxyAllowsReadWithoutWrite(t *testing.T) {
	peerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"deployment":{"name":"shop"}}`))
	}))
	defer peerServer.Close()

	env := setupClusterTestServer(t, "primary", true)
	defer env.cleanup()
	if err := env.server.clusterManager.AddPeer("remote", peerServer.URL, "key"); err != nil {
		t.Fatal(err)
	}
	user, err := env.server.authManager.CreateUser("fleet-reader", "", "password", auth.RoleService, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.server.authManager.CreateAPIKeyFromRaw(
		"fleet-reader-key", user.ID, "fleet-reader", "Fleet reader", auth.Role(""),
		[]string{auth.PermClusterRead.String()}, nil, time.Time{},
	)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/cluster/peers/remote/proxy/deployments/shop", nil)
	req.Header.Set("Authorization", "Bearer fleet-reader-key")
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("read status = %d, body = %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/cluster/peers/remote/proxy/deployments/shop/restart", nil)
	req.Header.Set("Authorization", "Bearer fleet-reader-key")
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("write status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestClusterProxyUnknownPeer(t *testing.T) {
	env := setupClusterTestServer(t, "primary", true)
	defer env.cleanup()

	token := clusterLogin(t, env.router)

	req := httptest.NewRequest(http.MethodGet, "/api/cluster/peers/unknown/proxy/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected 404 for unknown peer, got %d", w.Code)
	}
}

func TestClusterUnauthorizedAccess(t *testing.T) {
	env := setupClusterTestServer(t, "server", true)
	defer env.cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/cluster/status", nil)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401, got %d", w.Code)
	}
}

func TestClusterProvidersReportActiveAndAvailableAdapters(t *testing.T) {
	env := setupClusterTestServer(t, "server-a", true)
	defer env.cleanup()
	env.server.probeOrchestrator = func(_ context.Context, id orchestrator.ProviderID) error {
		if id == orchestrator.ProviderK3s {
			return fmt.Errorf("k3s adapter is not configured")
		}
		return nil
	}
	env.server.probeRouting = func(_ context.Context, id routing.ProviderID) error {
		if id == routing.ProviderTraefik {
			return fmt.Errorf("Traefik adapter is not configured")
		}
		return nil
	}

	req := httptest.NewRequest(http.MethodGet, "/api/cluster/providers", nil)
	req.Header.Set("Authorization", "Bearer "+clusterLogin(t, env.router))
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response clusterProvidersResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Orchestrators[0].Active || !response.Orchestrators[0].Available {
		t.Fatalf("unexpected standalone provider: %+v", response.Orchestrators[0])
	}
	if response.Orchestrators[2].Available || response.Orchestrators[2].Reason == "" {
		t.Fatalf("unexpected k3s provider: %+v", response.Orchestrators[2])
	}
	if !response.Routing[0].Active || !response.Routing[0].Available {
		t.Fatalf("unexpected Nginx provider: %+v", response.Routing[0])
	}
}

func TestUpdateClusterProvidersPersistsAvailableSelection(t *testing.T) {
	env := setupClusterTestServer(t, "server-a", true)
	defer env.cleanup()
	env.server.probeOrchestrator = func(_ context.Context, _ orchestrator.ProviderID) error { return nil }
	env.server.probeRouting = func(_ context.Context, _ routing.ProviderID) error { return nil }

	body := bytes.NewBufferString(`{"orchestrator":"swarm","routing":"nginx"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/cluster/providers", body)
	req.Header.Set("Authorization", "Bearer "+clusterLogin(t, env.router))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	saved, err := config.Load(env.server.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Cluster.Orchestrator != "swarm" || saved.Cluster.Routing != "nginx" {
		t.Fatalf("unexpected saved providers: %+v", saved.Cluster)
	}
}

func TestUpdateClusterProvidersPersistsK3sConnection(t *testing.T) {
	env := setupClusterTestServer(t, "server-a", true)
	defer env.cleanup()
	env.server.probeOrchestrator = func(_ context.Context, _ orchestrator.ProviderID) error { return nil }
	env.server.probeRouting = func(_ context.Context, _ routing.ProviderID) error { return nil }

	body := bytes.NewBufferString(`{"orchestrator":"k3s","routing":"traefik","k3s":{"kubeconfig":"/etc/rancher/k3s/k3s.yaml","namespace":"flatrun"}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/cluster/providers", body)
	req.Header.Set("Authorization", "Bearer "+clusterLogin(t, env.router))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	saved, err := config.Load(env.server.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Cluster.Routing != "traefik" || saved.Cluster.K3s.Kubeconfig != "/etc/rancher/k3s/k3s.yaml" || saved.Cluster.K3s.Namespace != "flatrun" {
		t.Fatalf("unexpected K3s connection: %+v", saved.Cluster.K3s)
	}
}

func TestUpdateClusterProvidersRejectsUnavailableSelection(t *testing.T) {
	env := setupClusterTestServer(t, "server-a", true)
	defer env.cleanup()
	env.server.probeOrchestrator = func(_ context.Context, id orchestrator.ProviderID) error {
		if id == orchestrator.ProviderK3s {
			return fmt.Errorf("k3s adapter is not configured")
		}
		return nil
	}
	env.server.probeRouting = func(_ context.Context, _ routing.ProviderID) error { return nil }

	body := bytes.NewBufferString(`{"orchestrator":"k3s","routing":"nginx"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/cluster/providers", body)
	req.Header.Set("Authorization", "Bearer "+clusterLogin(t, env.router))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
	}
	if env.server.config.Cluster.Orchestrator != "" {
		t.Fatalf("unexpected orchestrator change: %q", env.server.config.Cluster.Orchestrator)
	}
}

func TestClusterFullPeeringE2E(t *testing.T) {
	// This test simulates the full peering flow between two servers
	// using httptest servers as live HTTP endpoints.

	envA := setupClusterTestServer(t, "server-a", true)
	defer envA.cleanup()

	envB := setupClusterTestServer(t, "server-b", true)
	defer envB.cleanup()

	// Stand up real HTTP servers so the agents can reach each other
	httpServerA := httptest.NewServer(envA.router)
	defer httpServerA.Close()

	httpServerB := httptest.NewServer(envB.router)
	defer httpServerB.Close()

	tokenA := clusterLogin(t, envA.router)
	tokenB := clusterLogin(t, envB.router)

	// Step 1: Server A creates an invite
	req := httptest.NewRequest(http.MethodPost, "/api/cluster/invite", nil)
	req.Header.Set("Authorization", "Bearer "+tokenA)
	w := httptest.NewRecorder()
	envA.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Invite failed: %d - %s", w.Code, w.Body.String())
	}

	var inviteResp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &inviteResp)
	inviteToken := inviteResp["invite_token"].(string)

	// Step 2: Server B accepts the invite (contacts Server A via live HTTP)
	// callback_url tells A how to reach B back
	acceptBody, _ := json.Marshal(map[string]string{
		"invite_token": inviteToken,
		"peer_url":     httpServerA.URL,
		"callback_url": httpServerB.URL,
	})

	req = httptest.NewRequest(http.MethodPost, "/api/cluster/accept", bytes.NewBuffer(acceptBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tokenB)
	w = httptest.NewRecorder()
	envB.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Accept failed: %d - %s", w.Code, w.Body.String())
	}

	var acceptResp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &acceptResp)
	if acceptResp["peer_name"] != "server-a" {
		t.Errorf("Expected peer_name=server-a, got %v", acceptResp["peer_name"])
	}
	if acceptResp["status"] != "peered" {
		t.Errorf("Expected status=peered, got %v", acceptResp["status"])
	}

	// Step 3: Verify Server A sees Server B as a peer
	req = httptest.NewRequest(http.MethodGet, "/api/cluster/peers", nil)
	req.Header.Set("Authorization", "Bearer "+tokenA)
	w = httptest.NewRecorder()
	envA.router.ServeHTTP(w, req)

	var peersA map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &peersA)
	peersListA, _ := peersA["peers"].([]interface{})
	if len(peersListA) != 1 {
		t.Fatalf("Server A expected 1 peer, got %d", len(peersListA))
	}
	peerOnA := peersListA[0].(map[string]interface{})
	if peerOnA["name"] != "server-b" {
		t.Errorf("Server A peer name = %v, want server-b", peerOnA["name"])
	}

	// Step 4: Verify Server B sees Server A as a peer
	req = httptest.NewRequest(http.MethodGet, "/api/cluster/peers", nil)
	req.Header.Set("Authorization", "Bearer "+tokenB)
	w = httptest.NewRecorder()
	envB.router.ServeHTTP(w, req)

	var peersB map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &peersB)
	peersListB, _ := peersB["peers"].([]interface{})
	if len(peersListB) != 1 {
		t.Fatalf("Server B expected 1 peer, got %d", len(peersListB))
	}
	peerOnB := peersListB[0].(map[string]interface{})
	if peerOnB["name"] != "server-a" {
		t.Errorf("Server B peer name = %v, want server-a", peerOnB["name"])
	}

	// Step 5: Server A removes the peer
	req = httptest.NewRequest(http.MethodDelete, "/api/cluster/peers/server-b", nil)
	req.Header.Set("Authorization", "Bearer "+tokenA)
	w = httptest.NewRecorder()
	envA.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Remove peer failed: %d - %s", w.Code, w.Body.String())
	}

	// Step 6: Verify Server A no longer has the peer
	req = httptest.NewRequest(http.MethodGet, "/api/cluster/peers", nil)
	req.Header.Set("Authorization", "Bearer "+tokenA)
	w = httptest.NewRecorder()
	envA.router.ServeHTTP(w, req)

	var afterRemove map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &afterRemove)
	if afterRemove["peers"] != nil {
		peerList, ok := afterRemove["peers"].([]interface{})
		if ok && len(peerList) != 0 {
			t.Errorf("Server A expected 0 peers after removal, got %d", len(peerList))
		}
	}
}
