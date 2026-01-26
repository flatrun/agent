package auth

type Permission string

const (
	PermDeploymentsRead   Permission = "deployments:read"
	PermDeploymentsWrite  Permission = "deployments:write"
	PermDeploymentsDelete Permission = "deployments:delete"

	PermCertificatesRead   Permission = "certificates:read"
	PermCertificatesWrite  Permission = "certificates:write"
	PermCertificatesDelete Permission = "certificates:delete"

	PermNetworksRead   Permission = "networks:read"
	PermNetworksWrite  Permission = "networks:write"
	PermNetworksDelete Permission = "networks:delete"

	PermSecurityRead  Permission = "security:read"
	PermSecurityWrite Permission = "security:write"

	PermBackupsRead   Permission = "backups:read"
	PermBackupsWrite  Permission = "backups:write"
	PermBackupsDelete Permission = "backups:delete"

	PermUsersRead   Permission = "users:read"
	PermUsersWrite  Permission = "users:write"
	PermUsersDelete Permission = "users:delete"

	PermAPIKeysRead   Permission = "apikeys:read"
	PermAPIKeysWrite  Permission = "apikeys:write"
	PermAPIKeysDelete Permission = "apikeys:delete"

	PermSettingsRead  Permission = "settings:read"
	PermSettingsWrite Permission = "settings:write"

	PermAuditRead Permission = "audit:read"
)

var adminPermissions = []Permission{
	PermDeploymentsRead, PermDeploymentsWrite, PermDeploymentsDelete,
	PermCertificatesRead, PermCertificatesWrite, PermCertificatesDelete,
	PermNetworksRead, PermNetworksWrite, PermNetworksDelete,
	PermSecurityRead, PermSecurityWrite,
	PermBackupsRead, PermBackupsWrite, PermBackupsDelete,
	PermUsersRead, PermUsersWrite, PermUsersDelete,
	PermAPIKeysRead, PermAPIKeysWrite, PermAPIKeysDelete,
	PermSettingsRead, PermSettingsWrite,
	PermAuditRead,
}

var operatorPermissions = []Permission{
	PermDeploymentsRead, PermDeploymentsWrite,
	PermCertificatesRead, PermCertificatesWrite,
	PermNetworksRead,
	PermSecurityRead,
	PermBackupsRead, PermBackupsWrite,
	PermAPIKeysRead, PermAPIKeysWrite, PermAPIKeysDelete,
	PermSettingsRead,
}

var viewerPermissions = []Permission{
	PermDeploymentsRead,
	PermCertificatesRead,
	PermNetworksRead,
	PermSecurityRead,
	PermBackupsRead,
	PermAPIKeysRead,
	PermSettingsRead,
}

func GetRolePermissions(role Role) []Permission {
	switch role {
	case RoleAdmin:
		return adminPermissions
	case RoleOperator:
		return operatorPermissions
	case RoleViewer:
		return viewerPermissions
	default:
		return nil
	}
}

func HasPermission(role Role, explicitPerms []string, required Permission) bool {
	if role == RoleAdmin {
		return true
	}

	rolePerms := GetRolePermissions(role)
	for _, p := range rolePerms {
		if p == required {
			return true
		}
	}

	for _, p := range explicitPerms {
		if Permission(p) == required {
			return true
		}
	}

	return false
}

func GetAllPermissions() []Permission {
	return adminPermissions
}

func (p Permission) String() string {
	return string(p)
}
