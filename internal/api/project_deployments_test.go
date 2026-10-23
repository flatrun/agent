package api

import (
	"net/http"
	"testing"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/deploys"
)

func TestProjectDeploymentsListDeployRecords(t *testing.T) {
	s, client := setupWorkspaceTestServer(t, "shop")
	s.deploysStore = deploys.NewStore(s.config.DeploymentsPath)
	if _, err := s.authManager.CreateUser("carol", "", "carol-pass", auth.RoleOperator, nil); err != nil {
		t.Fatal(err)
	}
	admin := client.login("admin", "admin-pass")
	carol := client.login("carol", "carol-pass")

	first, err := s.deploysStore.Start("shop", deploys.Record{Trigger: deploys.TriggerPush})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.deploysStore.Finish("shop", first.ID, "1111111", "Add checkout", nil); err != nil {
		t.Fatal(err)
	}
	second, _ := s.deploysStore.Start("shop", deploys.Record{Trigger: deploys.TriggerManual})
	_ = s.deploysStore.Finish("shop", second.ID, "2222222", "Fix cart", nil)

	status, body := admin.do(http.MethodGet, "/projects/shop/deployments", "", nil)
	list, _ := body["deployments"].([]any)
	if status != http.StatusOK || len(list) != 2 {
		t.Fatalf("project deployments = %d %v", status, body)
	}
	if top := list[0].(map[string]any); top["id"] != second.ID || top["message"] != "Fix cart" {
		t.Fatalf("newest first = %v", top)
	}

	status, body = admin.do(http.MethodGet, "/projects/shop/deployments/"+first.ID, "", nil)
	if status != http.StatusOK || body["id"] != first.ID || body["revision"] != "1111111" {
		t.Fatalf("one deployment = %d %v", status, body)
	}
	if status, body := admin.do(http.MethodGet, "/projects/shop/deployments/"+first.ID+"/logs", "", nil); status != http.StatusOK || body["deploy"] == nil {
		t.Fatalf("deployment logs = %d %v", status, body)
	}
	if status, _ := admin.do(http.MethodGet, "/projects/shop/deployments/missing", "", nil); status != http.StatusNotFound {
		t.Fatalf("unknown deployment = %d", status)
	}
	if status, _ := carol.do(http.MethodGet, "/projects/shop/deployments", "", nil); status != http.StatusForbidden {
		t.Fatalf("outsider listing deployments = %d", status)
	}
	if status, body := admin.do(http.MethodGet, "/projects/shop/deploys", "", nil); status != http.StatusOK || body["deploys"] == nil {
		t.Fatalf("older deploys path = %d %v", status, body)
	}
}
