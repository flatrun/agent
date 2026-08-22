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
	"github.com/flatrun/agent/internal/autoscale"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/orchestrator"
	"github.com/flatrun/agent/internal/routing"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
)

func TestDeploymentAutoscalePolicyThroughHTTP(t *testing.T) {
	dir := t.TempDir()
	deploymentDir := filepath.Join(dir, "shop")
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deploymentDir, "compose.yml"), []byte("services:\n  app:\n    image: nginx:alpine\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deploymentDir, "service.yml"), []byte("name: shop\nscaling:\n  service: app\n  stateless: true\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{DeploymentsPath: dir, Auth: config.AuthConfig{Enabled: true, JWTSecret: "autoscale-test-secret"}}
	t.Setenv("FLATRUN_ADMIN_PASSWORD", "testadminpass")
	authManager, err := auth.NewManager(dir, &cfg.Auth, true)
	if err != nil {
		t.Fatal(err)
	}
	defer authManager.Close()
	store, err := autoscale.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	activated := ""
	server := &Server{
		config: cfg, manager: docker.NewManager(dir), authManager: authManager, autoscaleStore: store,
		runAutoscaleActivation: func(_ context.Context, name string) (autoscale.Activation, error) {
			activated = name
			return autoscale.Activation{
				Workload: orchestrator.Status{Workload: name, Desired: 2, Available: 2},
				Route:    routing.Route{ID: name, Domain: "shop.example.com", Protocol: "http"},
			}, nil
		},
	}
	middleware := auth.NewMiddlewareWithManager(&cfg.Auth, authManager)
	router := gin.New()
	router.POST("/api/auth/login", middleware.Login)
	protected := router.Group("/api", middleware.RequireAuth())
	protected.GET("/deployments/:name/autoscale", middleware.RequirePermission(auth.PermDeploymentsRead), middleware.RequireDeploymentAccess(auth.AccessLevelRead), server.getDeploymentAutoscalePolicy)
	protected.GET("/deployments/:name/autoscale/compatibility", middleware.RequirePermission(auth.PermDeploymentsRead), middleware.RequireDeploymentAccess(auth.AccessLevelRead), server.getDeploymentAutoscaleCompatibility)
	protected.PUT("/deployments/:name/autoscale/workload", middleware.RequirePermission(auth.PermDeploymentsWrite), middleware.RequireDeploymentAccess(auth.AccessLevelWrite), server.updateDeploymentAutoscaleWorkload)
	protected.PUT("/deployments/:name/autoscale", middleware.RequirePermission(auth.PermDeploymentsWrite), middleware.RequireDeploymentAccess(auth.AccessLevelWrite), server.updateDeploymentAutoscalePolicy)
	protected.POST("/deployments/:name/autoscale/activate", middleware.RequirePermission(auth.PermDeploymentsWrite), middleware.RequireDeploymentAccess(auth.AccessLevelWrite), server.activateDeploymentAutoscale)
	token := loginAndGetToken(t, router, "admin", "testadminpass")

	payload := autoscalePolicyRequest{
		Enabled: true, MinReplicas: 2, MaxReplicas: 6, ScaleUpPercent: 75, ScaleDownPercent: 25,
		ScaleUpWindows: 3, ScaleDownWindows: 8, CooldownSeconds: 120, AllowFleetCapacity: true,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/deployments/shop/autoscale", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/deployments/shop/autoscale", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response autoscalePolicyResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.MaxReplicas != 6 || response.CooldownSeconds != 120 || !response.AllowFleetCapacity {
		t.Fatalf("unexpected response: %+v", response)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/deployments/shop/autoscale/compatibility", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var compatibility autoscale.Compatibility
	if err := json.Unmarshal(w.Body.Bytes(), &compatibility); err != nil {
		t.Fatal(err)
	}
	if !compatibility.Compatible || compatibility.Service != "app" || compatibility.Image != "nginx:alpine" {
		t.Fatalf("compatibility = %#v", compatibility)
	}

	workload := bytes.NewBufferString(`{"service":"app","stateless":true,"storage":{"mode":"none"}}`)
	req = httptest.NewRequest(http.MethodPut, "/api/deployments/shop/autoscale/workload", workload)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &compatibility); err != nil {
		t.Fatal(err)
	}
	if !compatibility.Compatible || len(compatibility.Services) != 1 || compatibility.Services[0] != "app" {
		t.Fatalf("compatibility = %#v", compatibility)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/deployments/shop/autoscale/activate", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK || activated != "shop" {
		t.Fatalf("activation returned %d for %q: %s", w.Code, activated, w.Body.String())
	}
}

func TestCapacityClaimFitsWorkloadLimits(t *testing.T) {
	resources := orchestrator.Resources{CPULimit: 2, MemoryLimit: 2 << 30}
	claim := clusterCapacityClaimResponse{Node: orchestrator.NodeIdentity{ClusterID: "swarm-1"}, MaxCPU: 2, MaxMemory: 2 << 30}
	if !capacityClaimFits(claim, resources, "swarm-1") {
		t.Fatal("matching capacity grant was rejected")
	}
	claim.MaxCPU, claim.MaxMemory = 1, 4<<30
	if capacityClaimFits(claim, resources, "swarm-1") {
		t.Fatal("CPU limit above the capacity grant was accepted")
	}
	claim.MaxCPU, claim.MaxMemory = 4, 1<<30
	if capacityClaimFits(claim, resources, "swarm-1") {
		t.Fatal("memory limit above the capacity grant was accepted")
	}
	claim.MaxCPU, claim.MaxMemory = 4, 4<<30
	if capacityClaimFits(claim, resources, "swarm-2") {
		t.Fatal("capacity from a different Swarm was accepted")
	}
}
