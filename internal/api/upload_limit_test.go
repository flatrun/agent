package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/flatrun/agent/internal/nginx"
	"github.com/flatrun/agent/internal/proxy"
	"github.com/flatrun/agent/pkg/models"
)

func uploadLimitRouter(t *testing.T) (*Server, *gin.Engine) {
	t.Helper()
	server, tmpDir, cleanup := setupDomainsTestServer(t)
	t.Cleanup(cleanup)
	createTestDeployment(t, tmpDir, "shop", &models.ServiceMetadata{
		Name: "shop",
		Type: "web",
		Domains: []models.DomainConfig{
			{ID: "media", Service: "web", ContainerPort: 80, Domain: "media.example.com", ClientMaxBodySize: "64m", UploadLimitPage: true},
			{ID: "site", Service: "web", ContainerPort: 80, Domain: "shop.example.com"},
		},
	})
	router := gin.New()
	router.Any("/api/hooks/too-large/:deployment/:domain", server.requestTooLarge)
	router.GET("/api/deployments/:name/upload-rejections", server.listUploadRejections)
	return server, router
}

func withTestProxy(t *testing.T, server *Server) {
	t.Helper()
	cfg := *server.config
	cfg.DeploymentsPath = t.TempDir()
	server.proxyOrchestrator = proxy.NewOrchestrator(&cfg)
}

func tooLargeRequest(server *Server, path string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set(nginx.HookTokenHeader(), server.proxyOrchestrator.NginxManager().HookToken())
	return req
}

func TestTooLargeHookAnswersAPIClientsWithTheLimit(t *testing.T) {
	server, router := uploadLimitRouter(t)
	withTestProxy(t, server)

	req := tooLargeRequest(server, "/api/hooks/too-large/shop/media")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Original-URI", "/wp-admin/async-upload.php?x=1")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type = %q", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["limit"] != "64m" {
		t.Fatalf("body = %v, want the 64m limit", body)
	}
}

func TestTooLargeHookShowsLimitPageToBrowsers(t *testing.T) {
	server, router := uploadLimitRouter(t)
	withTestProxy(t, server)

	req := tooLargeRequest(server, "/api/hooks/too-large/shop/media")
	req.Header.Set("Accept", "text/html")
	req.Header.Set("X-Original-Host", "media.example.com")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "<strong>64m</strong>") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "/deployments/") {
		t.Fatal("the visitor page must not link to the admin settings")
	}
}

func TestTooLargeHookKeepsNginxResponseWhenPageIsOff(t *testing.T) {
	server, router := uploadLimitRouter(t)
	withTestProxy(t, server)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, tooLargeRequest(server, "/api/hooks/too-large/shop/site"))

	if rec.Code != http.StatusRequestEntityTooLarge || rec.Body.String() != nginxTooLargeBody {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
}

func TestUploadRejectionsListTheLimitAndCount(t *testing.T) {
	server, router := uploadLimitRouter(t)
	withTestProxy(t, server)

	for i := 0; i < 3; i++ {
		req := tooLargeRequest(server, "/api/hooks/too-large/shop/site")
		req.Header.Set("X-Original-URI", "/upload?token=secret")
		router.ServeHTTP(httptest.NewRecorder(), req)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/deployments/shop/upload-rejections", nil))
	var resp struct {
		Rejections []UploadRejection `json:"rejections"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Rejections) != 1 {
		t.Fatalf("rejections = %+v", resp.Rejections)
	}
	got := resp.Rejections[0]
	if got.DomainID != "site" || got.Count != 3 || got.Limit != "" || got.LastPath != "/upload" {
		t.Fatalf("rejection = %+v", got)
	}
}

func TestDomainRejectsInvalidUploadLimit(t *testing.T) {
	server, router := uploadLimitRouter(t)
	router.PATCH("/api/deployments/:name/domains/:domainId", server.patchDomain)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/api/deployments/shop/domains/site", strings.NewReader(`{"client_max_body_size":"64mb"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	deployment, err := server.manager.GetDeployment("shop")
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Metadata.Domains[1].ClientMaxBodySize != "" {
		t.Fatalf("an invalid limit was saved: %+v", deployment.Metadata.Domains[1])
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/api/deployments/shop/domains/site", strings.NewReader(`{"client_max_body_size":"128m"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	deployment, _ = server.manager.GetDeployment("shop")
	if deployment.Metadata.Domains[1].ClientMaxBodySize != "128m" {
		t.Fatalf("limit not saved: %+v", deployment.Metadata.Domains[1])
	}
}

func TestAgentUploadLimitDefaultIsValidatedBeforeSaving(t *testing.T) {
	s, _, ts := setupPlanTestServer(t)

	resp, parsed := doJSON(t, http.MethodPut, ts.URL+"/api/config/nginx.client_max_body_size", map[string]interface{}{"value": "lots"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, body %v", resp.StatusCode, parsed)
	}
	if s.config.Nginx.ClientMaxBodySize != "" {
		t.Fatalf("invalid default saved: %q", s.config.Nginx.ClientMaxBodySize)
	}

	resp, parsed = doJSON(t, http.MethodPut, ts.URL+"/api/config/nginx.client_max_body_size", map[string]interface{}{"value": "256m"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", resp.StatusCode, parsed)
	}
	if s.config.Nginx.ClientMaxBodySize != "256m" {
		t.Fatalf("default = %q", s.config.Nginx.ClientMaxBodySize)
	}
}

func TestTooLargeHookIgnoresCallsWithoutNginxKey(t *testing.T) {
	server, router := uploadLimitRouter(t)
	withTestProxy(t, server)

	for _, key := range []string{"", "forged"} {
		req := httptest.NewRequest(http.MethodGet, "/api/hooks/too-large/shop/site", nil)
		req.Header.Set(nginx.HookTokenHeader(), key)
		router.ServeHTTP(httptest.NewRecorder(), req)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/deployments/shop/upload-rejections", nil))
	if strings.Contains(rec.Body.String(), `"domain_id"`) {
		t.Fatalf("a call without nginx's key was recorded: %s", rec.Body.String())
	}
}
