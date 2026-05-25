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

	PermContainersRead   Permission = "containers:read"
	PermContainersWrite  Permission = "containers:write"
	PermContainersDelete Permission = "containers:delete"

	PermImagesRead   Permission = "images:read"
	PermImagesWrite  Permission = "images:write"
	PermImagesDelete Permission = "images:delete"

	PermVolumesRead   Permission = "volumes:read"
	PermVolumesWrite  Permission = "volumes:write"
	PermVolumesDelete Permission = "volumes:delete"

	PermDatabasesRead   Permission = "databases:read"
	PermDatabasesWrite  Permission = "databases:write"
	PermDatabasesDelete Permission = "databases:delete"

	PermInfrastructureRead  Permission = "infrastructure:read"
	PermInfrastructureWrite Permission = "infrastructure:write"

	PermSchedulerRead   Permission = "scheduler:read"
	PermSchedulerWrite  Permission = "scheduler:write"
	PermSchedulerDelete Permission = "scheduler:delete"

	PermSystemRead  Permission = "system:read"
	PermSystemWrite Permission = "system:write"
	PermSystemFiles Permission = "system:files"

	PermDNSRead  Permission = "dns:read"
	PermDNSWrite Permission = "dns:write"

	PermRegistriesRead   Permission = "registries:read"
	PermRegistriesWrite  Permission = "registries:write"
	PermRegistriesDelete Permission = "registries:delete"

	PermTemplatesRead  Permission = "templates:read"
	PermTemplatesWrite Permission = "templates:write"

	PermTrafficRead  Permission = "traffic:read"
	PermTrafficWrite Permission = "traffic:write"

	PermClusterRead  Permission = "cluster:read"
	PermClusterWrite Permission = "cluster:write"
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
	PermContainersRead, PermContainersWrite, PermContainersDelete,
	PermImagesRead, PermImagesWrite, PermImagesDelete,
	PermVolumesRead, PermVolumesWrite, PermVolumesDelete,
	PermDatabasesRead, PermDatabasesWrite, PermDatabasesDelete,
	PermInfrastructureRead, PermInfrastructureWrite,
	PermSchedulerRead, PermSchedulerWrite, PermSchedulerDelete,
	PermSystemRead, PermSystemWrite, PermSystemFiles,
	PermDNSRead, PermDNSWrite,
	PermRegistriesRead, PermRegistriesWrite, PermRegistriesDelete,
	PermTemplatesRead, PermTemplatesWrite,
	PermTrafficRead, PermTrafficWrite,
	PermClusterRead, PermClusterWrite,
}

var operatorPermissions = []Permission{
	PermDeploymentsRead, PermDeploymentsWrite,
	PermCertificatesRead, PermCertificatesWrite,
	PermNetworksRead,
	PermSecurityRead,
	PermBackupsRead, PermBackupsWrite,
	PermAPIKeysRead, PermAPIKeysWrite, PermAPIKeysDelete,
	PermSettingsRead,
	PermContainersRead, PermContainersWrite,
	PermImagesRead, PermImagesWrite,
	PermVolumesRead, PermVolumesWrite,
	PermDatabasesRead, PermDatabasesWrite,
	PermInfrastructureRead, PermInfrastructureWrite,
	PermSchedulerRead, PermSchedulerWrite,
	PermSystemRead, PermSystemWrite,
	PermDNSRead, PermDNSWrite,
	PermRegistriesRead, PermRegistriesWrite,
	PermTemplatesRead,
	PermTrafficRead,
}

var viewerPermissions = []Permission{
	PermDeploymentsRead,
	PermCertificatesRead,
	PermNetworksRead,
	PermSecurityRead,
	PermBackupsRead,
	PermAPIKeysRead,
	PermSettingsRead,
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

func EffectivePermissions(user *User, role Role) []Permission {
	if user != nil && len(user.Permissions) > 0 {
		perms := make([]Permission, 0, len(user.Permissions))
		for _, p := range user.Permissions {
			perms = append(perms, Permission(p))
		}
		return perms
	}
	return GetRolePermissions(role)
}
