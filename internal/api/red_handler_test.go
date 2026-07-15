package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flatrun/agent/internal/traffic"
	"github.com/gin-gonic/gin"
)

// TestDeploymentServingEndpoint drives the endpoint the serving charts read, against real
// traffic rows.
func TestDeploymentServingEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)

	manager, err := traffic.NewManager(t.TempDir(), 7)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	// The proxy reports every request through this path already.
	for i := 0; i < 4; i++ {
		status := 200
		if i == 3 {
			status = 500
		}
		if _, err := manager.IngestLog(&traffic.IngestTrafficLog{
			DeploymentName: "shop",
			RequestPath:    "/",
			RequestMethod:  "GET",
			StatusCode:     status,
			SourceIP:       "10.0.0.1",
			ResponseTimeMs: 20,
			BytesSent:      100,
			RequestLength:  50,
		}); err != nil {
			t.Fatal(err)
		}
	}

	s := &Server{trafficManager: manager}
	router := gin.New()
	router.GET("/deployments/:name/serving", s.getDeploymentRED)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/deployments/shop/serving?since=1h", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Deployment string             `json:"deployment"`
		Points     []traffic.REDPoint `json:"points"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Deployment != "shop" {
		t.Errorf("deployment = %q", body.Deployment)
	}
	if len(body.Points) == 0 {
		t.Fatal("no points for a deployment that just served requests")
	}

	var requests, errors int64
	for _, p := range body.Points {
		requests += p.Requests
		errors += p.Errors
	}
	if requests != 4 {
		t.Errorf("requests = %d, want 4", requests)
	}
	if errors != 1 {
		t.Errorf("errors = %d, want 1", errors)
	}
}

func TestDeploymentServingEndpointWithoutTraffic(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Traffic logging is optional, so the endpoint says so rather than panicking.
	s := &Server{}
	router := gin.New()
	router.GET("/deployments/:name/serving", s.getDeploymentRED)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/deployments/shop/serving", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}
