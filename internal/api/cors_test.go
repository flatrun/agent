package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/config"
)

func TestPeerDeploymentHeaderPassesCORSPreflight(t *testing.T) {
	server := New(&config.Config{
		DeploymentsPath: t.TempDir(),
		API: config.APIConfig{
			EnableCORS:     true,
			AllowedOrigins: []string{"https://panel.example.com"},
		},
	}, "")

	request := httptest.NewRequest(http.MethodOptions, "/api/cluster/peers/prod2/proxy/deployments/smartpings", nil)
	request.Header.Set("Origin", "https://panel.example.com")
	request.Header.Set("Access-Control-Request-Method", http.MethodGet)
	request.Header.Set("Access-Control-Request-Headers", "authorization,x-flatrun-deployment")
	response := httptest.NewRecorder()

	server.router.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want %d", response.Code, http.StatusNoContent)
	}
	allowed := strings.ToLower(response.Header().Get("Access-Control-Allow-Headers"))
	if !strings.Contains(allowed, "x-flatrun-deployment") {
		t.Fatalf("allowed headers = %q, want x-flatrun-deployment", allowed)
	}
}
