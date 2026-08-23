package auth

import (
	"testing"
)

func TestRoleIsValid(t *testing.T) {
	tests := []struct {
		role  Role
		valid bool
	}{
		{RoleAdmin, true},
		{RoleOperator, true},
		{RoleViewer, true},
		{Role("invalid"), false},
		{Role(""), false},
	}

	for _, tt := range tests {
		if got := tt.role.IsValid(); got != tt.valid {
			t.Errorf("Role(%q).IsValid() = %v, want %v", tt.role, got, tt.valid)
		}
	}
}

func TestGetRolePermissions(t *testing.T) {
	adminPerms := GetRolePermissions(RoleAdmin)
	if len(adminPerms) == 0 {
		t.Error("Admin should have permissions")
	}

	operatorPerms := GetRolePermissions(RoleOperator)
	if len(operatorPerms) == 0 {
		t.Error("Operator should have permissions")
	}
	for _, permission := range operatorPerms {
		if permission == PermSystemWrite || permission == PermSystemFiles {
			t.Fatalf("operator role includes host control permission %s", permission)
		}
	}

	viewerPerms := GetRolePermissions(RoleViewer)
	if len(viewerPerms) == 0 {
		t.Error("Viewer should have permissions")
	}

	if len(adminPerms) <= len(operatorPerms) {
		t.Error("Admin should have more permissions than operator")
	}

	if len(operatorPerms) <= len(viewerPerms) {
		t.Error("Operator should have more permissions than viewer")
	}

	invalidPerms := GetRolePermissions(Role("invalid"))
	if invalidPerms != nil {
		t.Error("Invalid role should return nil permissions")
	}
}

func TestAdminHasAllPermissions(t *testing.T) {
	allPerms := GetAllPermissions()

	for _, perm := range allPerms {
		if !HasPermission(RoleAdmin, nil, perm) {
			t.Errorf("Admin should have permission %s", perm)
		}
	}
}

func TestViewerCannotWrite(t *testing.T) {
	writePerms := []Permission{
		PermDeploymentsWrite,
		PermDeploymentsDelete,
		PermUsersWrite,
		PermUsersDelete,
		PermContainersWrite,
		PermContainersDelete,
		PermImagesWrite,
		PermImagesDelete,
		PermVolumesWrite,
		PermVolumesDelete,
		PermDatabasesWrite,
		PermDatabasesDelete,
		PermInfrastructureWrite,
		PermSchedulerWrite,
		PermSchedulerDelete,
		PermSystemWrite,
		PermDNSWrite,
		PermRegistriesWrite,
		PermRegistriesDelete,
		PermTemplatesWrite,
		PermTrafficWrite,
		PermStorageWrite,
		PermStorageDelete,
		PermNotificationsWrite,
		PermUpdatesRead,
		PermUpdatesWrite,
	}

	for _, perm := range writePerms {
		if HasPermission(RoleViewer, nil, perm) {
			t.Errorf("Viewer should not have permission %s", perm)
		}
	}
}

func TestViewerCanRead(t *testing.T) {
	readPerms := []Permission{
		PermDeploymentsRead,
		PermCertificatesRead,
		PermNetworksRead,
		PermContainersRead,
		PermImagesRead,
		PermVolumesRead,
		PermDatabasesRead,
		PermInfrastructureRead,
		PermSchedulerRead,
		PermSystemRead,
		PermDNSRead,
		PermRegistriesRead,
		PermTemplatesRead,
		PermTrafficRead,
		PermStorageRead,
	}

	for _, perm := range readPerms {
		if !HasPermission(RoleViewer, nil, perm) {
			t.Errorf("Viewer should have permission %s", perm)
		}
	}
}

func TestOperatorPermissions(t *testing.T) {
	if !HasPermission(RoleOperator, nil, PermDeploymentsWrite) {
		t.Error("Operator should be able to write deployments")
	}

	if HasPermission(RoleOperator, nil, PermUsersWrite) {
		t.Error("Operator should not be able to write users")
	}

	if HasPermission(RoleOperator, nil, PermDeploymentsDelete) {
		t.Error("Operator should not be able to delete deployments")
	}

	if HasPermission(RoleOperator, nil, PermUpdatesRead) || HasPermission(RoleOperator, nil, PermUpdatesWrite) {
		t.Error("Operator should not have update permissions by default")
	}

	adminOnlyPerms := []Permission{
		PermSettingsRead,
		PermSettingsWrite,
		PermNotificationsRead,
		PermNotificationsWrite,
		PermAPIKeysRead,
		PermAPIKeysWrite,
		PermAPIKeysDelete,
	}
	for _, perm := range adminOnlyPerms {
		if HasPermission(RoleOperator, nil, perm) {
			t.Errorf("Operator should not have permission %s without an explicit grant", perm)
		}
		if HasPermission(RoleViewer, nil, perm) {
			t.Errorf("Viewer should not have permission %s without an explicit grant", perm)
		}
	}

	// Operator can write new resource groups
	operatorWritePerms := []Permission{
		PermContainersWrite,
		PermImagesWrite,
		PermVolumesWrite,
		PermDatabasesWrite,
		PermInfrastructureWrite,
		PermSchedulerWrite,
		PermDNSWrite,
		PermRegistriesWrite,
		PermStorageWrite,
	}
	for _, perm := range operatorWritePerms {
		if !HasPermission(RoleOperator, nil, perm) {
			t.Errorf("Operator should have permission %s", perm)
		}
	}

	// Operator cannot delete new resource groups
	operatorNoDeletePerms := []Permission{
		PermContainersDelete,
		PermImagesDelete,
		PermVolumesDelete,
		PermDatabasesDelete,
		PermSchedulerDelete,
		PermRegistriesDelete,
		PermStorageDelete,
	}
	for _, perm := range operatorNoDeletePerms {
		if HasPermission(RoleOperator, nil, perm) {
			t.Errorf("Operator should not have permission %s", perm)
		}
	}
}

func TestExplicitPermissionsOverride(t *testing.T) {
	explicitPerms := []string{string(PermUsersWrite)}

	if !HasPermission(RoleViewer, explicitPerms, PermUsersWrite) {
		t.Error("Explicit permission should grant access")
	}

	if HasPermission(RoleViewer, explicitPerms, PermUsersDelete) {
		t.Error("Should not have permissions not explicitly granted")
	}
}

func TestPermissionString(t *testing.T) {
	perm := PermDeploymentsRead

	if perm.String() != "deployments:read" {
		t.Errorf("Permission.String() = %s, want deployments:read", perm.String())
	}
}
