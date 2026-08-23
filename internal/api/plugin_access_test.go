package api

import (
	"net/http"
	"testing"

	"github.com/flatrun/agent/internal/auth"
)

func TestPluginPermissionSeparatesReadsAndWrites(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		method string
		want   auth.Permission
	}{
		{name: "observability", path: "/alerts/rules", method: http.MethodGet, want: auth.PermAlertsRead},
		{name: "observability", path: "/alerts/rules", method: http.MethodPut, want: auth.PermAlertsWrite},
		{name: "example", path: "/config", method: http.MethodGet, want: auth.PermTemplatesRead},
		{name: "example", path: "/config", method: http.MethodPut, want: auth.PermTemplatesWrite},
	}
	for _, test := range tests {
		if got := pluginPermission(test.name, test.path, test.method); got != test.want {
			t.Errorf("%s %s %s: got %s, want %s", test.method, test.name, test.path, got, test.want)
		}
	}
}

func TestActorResourceAccessCarriesDeploymentLevels(t *testing.T) {
	access := actorResourceAccess(&auth.ActorContext{
		Role: auth.RoleOperator,
		Deployments: map[string]string{
			"shop":    auth.AccessLevelWrite,
			"billing": auth.AccessLevelRead,
		},
	})
	if !access.Allows("deployment", "shop", "write") {
		t.Fatal("shop write access missing")
	}
	if !access.Allows("deployment", "billing", "read") || access.Allows("deployment", "billing", "write") {
		t.Fatal("billing read access widened")
	}
}

func TestActorResourceAccessHonoursAPIKeyScope(t *testing.T) {
	access := actorResourceAccess(&auth.ActorContext{
		Role: auth.RoleAdmin,
		User: &auth.User{Role: auth.RoleAdmin},
		APIKey: &auth.APIKey{Deployments: auth.DeploymentAccess{
			"shop": auth.AccessLevelRead,
		}},
	})
	if access.Global || !access.Allows("deployment", "shop", "read") {
		t.Fatal("scoped admin key did not retain its deployment read grant")
	}
	if access.Allows("deployment", "shop", "write") || access.Allows("deployment", "billing", "read") {
		t.Fatal("scoped admin key was widened")
	}
}
