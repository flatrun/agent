package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
)

// The object store template must carry its declared S3 bootstrap contract
// through the list API, which is what the picker forwards to auto-register it.
func TestListTemplates_ObjectStoreCarriesContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	srv := &Server{config: &config.Config{DeploymentsPath: dir}}
	seedRepoTemplate(t, dir, "minio")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/templates", nil)
	srv.listTemplates(c)

	var resp struct {
		Templates []Template `json:"templates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var minio *Template
	for i := range resp.Templates {
		if resp.Templates[i].ID == "minio" {
			minio = &resp.Templates[i]
		}
	}
	if minio == nil {
		t.Fatal("minio template missing from type=all listing")
	}
	if minio.ObjectStore == nil {
		t.Fatal("minio template does not carry an object_store contract")
	}
	if minio.ObjectStore.APIPort != 9000 || minio.ObjectStore.AccessKeyEnv != "MINIO_ROOT_USER" {
		t.Fatalf("unexpected contract: %+v", *minio.ObjectStore)
	}
}

// A storage template is a normal, standalone template: it must appear in the
// default catalog listing, not only under type=all.
func TestListTemplates_ObjectStoreVisibleInDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	srv := &Server{config: &config.Config{DeploymentsPath: dir}}
	seedRepoTemplate(t, dir, "minio")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/templates", nil)
	srv.listTemplates(c)

	var resp struct {
		Templates []Template `json:"templates"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	for _, tpl := range resp.Templates {
		if tpl.ID == "minio" {
			return
		}
	}
	t.Fatal("minio (storage category) should appear in the default listing")
}
