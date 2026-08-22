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
	"github.com/flatrun/agent/internal/autoscale"
	"github.com/flatrun/agent/internal/docker"
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
	server := &Server{config: cfg, manager: docker.NewManager(dir), authManager: authManager, autoscaleStore: store}
	middleware := auth.NewMiddlewareWithManager(&cfg.Auth, authManager)
	router := gin.New()
	router.POST("/api/auth/login", middleware.Login)
	protected := router.Group("/api", middleware.RequireAuth())
	protected.GET("/deployments/:name/autoscale", middleware.RequirePermission(auth.PermDeploymentsRead), middleware.RequireDeploymentAccess(auth.AccessLevelRead), server.getDeploymentAutoscalePolicy)
	protected.PUT("/deployments/:name/autoscale", middleware.RequirePermission(auth.PermDeploymentsWrite), middleware.RequireDeploymentAccess(auth.AccessLevelWrite), server.updateDeploymentAutoscalePolicy)
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
}
