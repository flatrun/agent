package auth

import (
	"testing"
)

func TestActorContextHasPermission(t *testing.T) {
	tests := []struct {
		name       string
		actor      *ActorContext
		permission Permission
		want       bool
	}{
		{
			name:       "admin has all permissions",
			actor:      &ActorContext{Role: RoleAdmin},
			permission: PermUsersDelete,
			want:       true,
		},
		{
			name:       "viewer has read permission",
			actor:      &ActorContext{Role: RoleViewer},
			permission: PermDeploymentsRead,
			want:       true,
		},
		{
			name:       "viewer cannot write",
			actor:      &ActorContext{Role: RoleViewer},
			permission: PermDeploymentsWrite,
			want:       false,
		},
		{
			name: "explicit permission overrides role",
			actor: &ActorContext{
				Role:        RoleViewer,
				Permissions: []string{string(PermUsersWrite)},
			},
			permission: PermUsersWrite,
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.actor.HasPermission(tt.permission); got != tt.want {
				t.Errorf("ActorContext.HasPermission() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestActorContextCanAccessDeployment(t *testing.T) {
	tests := []struct {
		name           string
		actor          *ActorContext
		deploymentName string
		requiredLevel  string
		want           bool
	}{
		{
			name:           "admin can access any deployment",
			actor:          &ActorContext{Role: RoleAdmin},
			deploymentName: "any-deployment",
			requiredLevel:  "admin",
			want:           true,
		},
		{
			name: "user with read access can read",
			actor: &ActorContext{
				Role:        RoleOperator,
				Deployments: map[string]string{"my-app": "read"},
			},
			deploymentName: "my-app",
			requiredLevel:  "read",
			want:           true,
		},
		{
			name: "user with read access cannot write",
			actor: &ActorContext{
				Role:        RoleOperator,
				Deployments: map[string]string{"my-app": "read"},
			},
			deploymentName: "my-app",
			requiredLevel:  "write",
			want:           false,
		},
		{
			name: "user with write access can read",
			actor: &ActorContext{
				Role:        RoleOperator,
				Deployments: map[string]string{"my-app": "write"},
			},
			deploymentName: "my-app",
			requiredLevel:  "read",
			want:           true,
		},
		{
			name: "user with write access can write",
			actor: &ActorContext{
				Role:        RoleOperator,
				Deployments: map[string]string{"my-app": "write"},
			},
			deploymentName: "my-app",
			requiredLevel:  "write",
			want:           true,
		},
		{
			name: "user cannot access unassigned deployment",
			actor: &ActorContext{
				Role:        RoleOperator,
				Deployments: map[string]string{"my-app": "write"},
			},
			deploymentName: "other-app",
			requiredLevel:  "read",
			want:           false,
		},
		{
			name: "api key scoped to deployment restricts access",
			actor: &ActorContext{
				Role:        RoleOperator,
				Deployments: map[string]string{"app-a": "write", "app-b": "write"},
				APIKey:      &APIKey{Deployments: DeploymentAccess{"app-a": AccessLevelAdmin}},
			},
			deploymentName: "app-b",
			requiredLevel:  "read",
			want:           false,
		},
		{
			name: "api key scoped to deployment allows access",
			actor: &ActorContext{
				Role:        RoleOperator,
				Deployments: map[string]string{"app-a": "write"},
				APIKey:      &APIKey{Deployments: DeploymentAccess{"app-a": AccessLevelAdmin}},
			},
			deploymentName: "app-a",
			requiredLevel:  "write",
			want:           true,
		},
		{
			name: "admin user with deployment-scoped non-admin key gets the key's level",
			actor: &ActorContext{
				User:   &User{Role: RoleAdmin},
				Role:   RoleOperator,
				APIKey: &APIKey{Deployments: DeploymentAccess{"my-app": AccessLevelWrite}},
			},
			deploymentName: "my-app",
			requiredLevel:  "write",
			want:           true,
		},
		{
			name: "admin user with deployment-scoped key cannot exceed the key's level",
			actor: &ActorContext{
				User:   &User{Role: RoleAdmin},
				Role:   RoleOperator,
				APIKey: &APIKey{Deployments: DeploymentAccess{"my-app": AccessLevelWrite}},
			},
			deploymentName: "my-app",
			requiredLevel:  "admin",
			want:           false,
		},
		{
			name: "admin user with deployment-scoped key denies unlisted deployment",
			actor: &ActorContext{
				User:   &User{Role: RoleAdmin},
				Role:   RoleOperator,
				APIKey: &APIKey{Deployments: DeploymentAccess{"my-app": AccessLevelWrite}},
			},
			deploymentName: "other-app",
			requiredLevel:  "read",
			want:           false,
		},
		{
			name: "admin-role key with deployment scope is capped by the scope",
			actor: &ActorContext{
				User:   &User{Role: RoleAdmin},
				Role:   RoleAdmin,
				APIKey: &APIKey{Role: RoleAdmin, Deployments: DeploymentAccess{"my-app": AccessLevelWrite}},
			},
			deploymentName: "my-app",
			requiredLevel:  "admin",
			want:           false,
		},
		{
			name: "admin user with unscoped key keeps admin access",
			actor: &ActorContext{
				User:   &User{Role: RoleAdmin},
				Role:   RoleOperator,
				APIKey: &APIKey{},
			},
			deploymentName: "anything",
			requiredLevel:  "admin",
			want:           true,
		},
		{
			name: "operator user cannot gain access via the key alone",
			actor: &ActorContext{
				User:   &User{Role: RoleOperator},
				Role:   RoleOperator,
				APIKey: &APIKey{Deployments: DeploymentAccess{"my-app": AccessLevelWrite}},
			},
			deploymentName: "my-app",
			requiredLevel:  "read",
			want:           false,
		},
		{
			name: "operator user with both grants takes the lower level",
			actor: &ActorContext{
				User:        &User{Role: RoleOperator},
				Role:        RoleOperator,
				Deployments: map[string]string{"my-app": "read"},
				APIKey:      &APIKey{Deployments: DeploymentAccess{"my-app": AccessLevelAdmin}},
			},
			deploymentName: "my-app",
			requiredLevel:  "write",
			want:           false,
		},
		{
			name: "anonymous admin (no user, no key) keeps admin access",
			actor: &ActorContext{
				Role: RoleAdmin,
			},
			deploymentName: "any",
			requiredLevel:  "admin",
			want:           true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.actor.CanAccessDeployment(tt.deploymentName, tt.requiredLevel); got != tt.want {
				t.Errorf("ActorContext.CanAccessDeployment() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAccessLevelSufficient(t *testing.T) {
	tests := []struct {
		has      string
		required string
		want     bool
	}{
		{"read", "read", true},
		{"write", "read", true},
		{"write", "write", true},
		{"admin", "read", true},
		{"admin", "write", true},
		{"admin", "admin", true},
		{"read", "write", false},
		{"read", "admin", false},
		{"write", "admin", false},
	}

	for _, tt := range tests {
		t.Run(tt.has+"_vs_"+tt.required, func(t *testing.T) {
			if got := accessLevelSufficient(tt.has, tt.required); got != tt.want {
				t.Errorf("accessLevelSufficient(%s, %s) = %v, want %v", tt.has, tt.required, got, tt.want)
			}
		})
	}
}

func TestAPIKeyGetPermissionsJSON(t *testing.T) {
	key := &APIKey{Permissions: []string{"deployments:read", "deployments:write"}}

	json := key.GetPermissionsJSON()
	if json == "" {
		t.Error("GetPermissionsJSON returned empty for non-empty permissions")
	}

	emptyKey := &APIKey{}
	if emptyKey.GetPermissionsJSON() != "" {
		t.Error("GetPermissionsJSON should return empty for nil permissions")
	}
}

func TestAPIKeyGetDeploymentsJSON(t *testing.T) {
	key := &APIKey{Deployments: DeploymentAccess{"app-a": AccessLevelAdmin, "app-b": AccessLevelRead}}

	json := key.GetDeploymentsJSON()
	if json == "" {
		t.Error("GetDeploymentsJSON returned empty for non-empty deployments")
	}

	emptyKey := &APIKey{}
	if emptyKey.GetDeploymentsJSON() != "" {
		t.Error("GetDeploymentsJSON should return empty for nil deployments")
	}
}

func TestParsePermissionsJSON(t *testing.T) {
	json := `["deployments:read","deployments:write"]`
	perms := ParsePermissionsJSON(json)

	if len(perms) != 2 {
		t.Errorf("ParsePermissionsJSON returned %d items, want 2", len(perms))
	}

	if perms[0] != "deployments:read" {
		t.Errorf("First permission = %s, want deployments:read", perms[0])
	}

	empty := ParsePermissionsJSON("")
	if empty != nil {
		t.Error("ParsePermissionsJSON should return nil for empty string")
	}
}

func TestParseDeploymentsJSON(t *testing.T) {
	json := `["app-a","app-b"]`
	deps := ParseDeploymentsJSON(json)

	if len(deps) != 2 {
		t.Errorf("ParseDeploymentsJSON returned %d items, want 2", len(deps))
	}

	empty := ParseDeploymentsJSON("")
	if empty != nil {
		t.Error("ParseDeploymentsJSON should return nil for empty string")
	}
}
