package api

import (
	"net/http"
	"testing"

	"github.com/flatrun/agent/pkg/models"
)

func TestUpdateDeploymentMetadataKeepsSettingsWhenProxyRejectsThem(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	s.config.Nginx.Enabled = true
	createTestDeployment(t, tmpDir, "shop", &models.ServiceMetadata{Name: "shop", Type: "web"})

	resp, parsed := doJSON(t, http.MethodPut, ts.URL+"/api/deployments/shop/metadata",
		map[string]interface{}{
			"networking": map[string]interface{}{
				"expose":         true,
				"domain":         "shop.example.com",
				"container_port": 80,
			},
		})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 when nginx cannot take the config; body %v", resp.StatusCode, parsed)
	}

	deployment, err := s.manager.GetDeployment("shop")
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Metadata.Networking.Expose || deployment.Metadata.Networking.Domain != "" {
		t.Fatalf("networking = %+v, want the previous settings kept", deployment.Metadata.Networking)
	}
}

func TestUpdateDeploymentMetadataSavesWhenFlatRunDoesNotManageNginx(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	s.config.Nginx.Enabled = false
	createTestDeployment(t, tmpDir, "shop", &models.ServiceMetadata{Name: "shop", Type: "web"})

	resp, parsed := doJSON(t, http.MethodPut, ts.URL+"/api/deployments/shop/metadata",
		map[string]interface{}{
			"logo": "shop.svg",
			"networking": map[string]interface{}{
				"expose":         true,
				"domain":         "shop.example.com",
				"container_port": 80,
			},
		})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 without a managed nginx; body %v", resp.StatusCode, parsed)
	}
	deployment, err := s.manager.GetDeployment("shop")
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Metadata.Logo != "shop.svg" || deployment.Metadata.Networking.Domain != "shop.example.com" {
		t.Fatalf("metadata = %+v, want the update saved", deployment.Metadata)
	}
}
