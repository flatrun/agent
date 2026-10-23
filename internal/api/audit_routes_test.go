package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flatrun/agent/internal/audit"
	"github.com/gin-gonic/gin"
)

func TestSecretAndWorkspaceRoutesAreAuditedWithoutBodies(t *testing.T) {
	s, _ := setupWorkspaceTestServer(t, "shop")
	manager, err := audit.NewManager(t.TempDir(), &audit.Config{Enabled: true, CaptureRequestBody: true, SensitiveFields: []string{"password", "token", "secret"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	s.auditManager = manager
	s.auditMiddleware = audit.NewMiddleware(manager)
	s.router = gin.New()
	s.setupRoutes()
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	admin := workspaceClient{t: t, url: ts.URL}.login("admin", "admin-pass")

	if status, body := admin.do(http.MethodPut, "/secrets/DB_PASSWORD?scope=deployment:shop", "", map[string]string{"value": "correct-horse"}); status != http.StatusOK {
		t.Fatalf("set secret = %d %v", status, body)
	}
	if status, body := admin.do(http.MethodPost, "/workspaces", "", map[string]string{"name": "Acme"}); status != http.StatusCreated {
		t.Fatalf("workspace = %d %v", status, body)
	}

	routes := []string{"PUT /api/secrets/DB_PASSWORD", "POST /api/workspaces"}
	seen := map[string]audit.AuditEvent{}
	waitFor(func() bool {
		events, _, err := manager.GetEvents(&audit.AuditFilter{Limit: 100})
		if err != nil {
			return false
		}
		for _, e := range events {
			seen[e.Method+" "+e.Path] = e
		}
		for _, route := range routes {
			if _, ok := seen[route]; !ok {
				return false
			}
		}
		return true
	})
	for _, route := range routes {
		event, ok := seen[route]
		if !ok {
			t.Errorf("%s was not audited; recorded: %v", route, seen)
			continue
		}
		if event.RequestBody != "" {
			t.Errorf("%s recorded its body: %s", route, event.RequestBody)
		}
	}
	for _, e := range seen {
		if strings.Contains(e.RequestBody, "correct-horse") {
			t.Errorf("audit log holds the secret value: %+v", e)
		}
	}
}
