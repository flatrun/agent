package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

func TestMigrationWorkflowPersistsThroughHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	createTestDeployment(t, dir, "shop", &models.ServiceMetadata{Name: "shop"})
	server := &Server{manager: docker.NewManager(dir)}
	router := gin.New()
	router.PUT("/deployments/:name/migration", server.updateDeploymentMigration)
	router.GET("/deployments/:name/migration", server.getDeploymentMigration)

	body := bytes.NewBufferString(`{"source":"legacy","inventory_complete":true,"expected_address":"192.0.2.10","sites":[{"hostname":"shop.example.com","source_path":"/srv/shop","bytes":42,"transferred":true}]}`)
	req := httptest.NewRequest(http.MethodPut, "/deployments/shop/migration", body)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("update returned %d: %s", res.Code, res.Body.String())
	}

	res = httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/deployments/shop/migration", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("get returned %d: %s", res.Code, res.Body.String())
	}
	var response struct {
		Migration migrationStatus `json:"migration"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Migration.Plan == nil || response.Migration.Plan.Sites[0].Bytes != 42 {
		t.Fatalf("migration was not persisted: %s", res.Body.String())
	}
	if response.Migration.RetirementReady {
		t.Fatal("migration became ready before final sync and cutover")
	}
}
