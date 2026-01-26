package auth

import (
	"encoding/json"
	"time"
)

type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
)

func (r Role) IsValid() bool {
	switch r {
	case RoleAdmin, RoleOperator, RoleViewer:
		return true
	}
	return false
}

type User struct {
	ID           int64     `json:"id"`
	UID          string    `json:"uid"`
	Username     string    `json:"username"`
	Email        string    `json:"email,omitempty"`
	PasswordHash string    `json:"-"`
	Role         Role      `json:"role"`
	IsActive     bool      `json:"is_active"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	LastLoginAt  time.Time `json:"last_login_at,omitempty"`
}

type APIKey struct {
	ID          int64     `json:"id"`
	KeyID       string    `json:"key_id"`
	UserID      int64     `json:"user_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	KeyHash     string    `json:"-"`
	KeyPrefix   string    `json:"key_prefix"`
	Role        Role      `json:"role,omitempty"`
	Permissions []string  `json:"permissions,omitempty"`
	Deployments []string  `json:"deployments,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
	LastUsedAt  time.Time `json:"last_used_at,omitempty"`
	LastUsedIP  string    `json:"last_used_ip,omitempty"`
	IsActive    bool      `json:"is_active"`
	CreatedAt   time.Time `json:"created_at"`
}

type Session struct {
	ID        int64     `json:"id"`
	SessionID string    `json:"session_id"`
	UserID    int64     `json:"user_id"`
	APIKeyID  int64     `json:"api_key_id,omitempty"`
	TokenHash string    `json:"-"`
	ExpiresAt time.Time `json:"expires_at"`
	RevokedAt time.Time `json:"revoked_at,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ClientIP  string    `json:"client_ip,omitempty"`
}

type UserDeployment struct {
	ID             int64     `json:"id"`
	UserID         int64     `json:"user_id"`
	DeploymentName string    `json:"deployment_name"`
	AccessLevel    string    `json:"access_level"`
	GrantedBy      int64     `json:"granted_by,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type ActorContext struct {
	Type        string             `json:"type"`
	UserID      int64              `json:"user_id,omitempty"`
	User        *User              `json:"user,omitempty"`
	APIKey      *APIKey            `json:"api_key,omitempty"`
	Role        Role               `json:"role"`
	Permissions []string           `json:"permissions,omitempty"`
	Deployments map[string]string  `json:"deployments,omitempty"`
}

func (a *ActorContext) HasPermission(p Permission) bool {
	if a.Role == RoleAdmin {
		return true
	}

	rolePerms := GetRolePermissions(a.Role)
	for _, rp := range rolePerms {
		if rp == p {
			return true
		}
	}

	for _, ep := range a.Permissions {
		if Permission(ep) == p {
			return true
		}
	}

	return false
}

func (a *ActorContext) CanAccessDeployment(name string, requiredLevel string) bool {
	if a.Role == RoleAdmin {
		return true
	}

	if a.APIKey != nil && len(a.APIKey.Deployments) > 0 {
		found := false
		for _, d := range a.APIKey.Deployments {
			if d == name {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	level, ok := a.Deployments[name]
	if !ok {
		return false
	}

	return accessLevelSufficient(level, requiredLevel)
}

func accessLevelSufficient(has, required string) bool {
	levels := map[string]int{
		"read":  1,
		"write": 2,
		"admin": 3,
	}
	return levels[has] >= levels[required]
}

func (a *APIKey) GetPermissionsJSON() string {
	if len(a.Permissions) == 0 {
		return ""
	}
	b, _ := json.Marshal(a.Permissions)
	return string(b)
}

func (a *APIKey) GetDeploymentsJSON() string {
	if len(a.Deployments) == 0 {
		return ""
	}
	b, _ := json.Marshal(a.Deployments)
	return string(b)
}

func ParsePermissionsJSON(s string) []string {
	if s == "" {
		return nil
	}
	var perms []string
	_ = json.Unmarshal([]byte(s), &perms)
	return perms
}

func ParseDeploymentsJSON(s string) []string {
	if s == "" {
		return nil
	}
	var deps []string
	_ = json.Unmarshal([]byte(s), &deps)
	return deps
}
