package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
	RoleService  Role = "service"
)

const (
	AccessLevelRead  = "read"
	AccessLevelWrite = "write"
	AccessLevelAdmin = "admin"
)

func ValidAccessLevel(level string) bool {
	return level == AccessLevelRead || level == AccessLevelWrite || level == AccessLevelAdmin
}

func (r Role) IsValid() bool {
	switch r {
	case RoleAdmin, RoleOperator, RoleViewer, RoleService:
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
	Permissions  []string  `json:"permissions,omitempty"`
	IsActive     bool      `json:"is_active"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	LastLoginAt  time.Time `json:"last_login_at,omitempty"`
}

func (u *User) GetPermissionsJSON() string {
	if len(u.Permissions) == 0 {
		return ""
	}
	b, _ := json.Marshal(u.Permissions)
	return string(b)
}

type DeploymentAccess map[string]string

func (d *DeploymentAccess) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*d = nil
		return nil
	}
	if trimmed[0] == '{' {
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		*d = m
		return nil
	}
	if trimmed[0] == '[' {
		var arr []string
		if err := json.Unmarshal(data, &arr); err != nil {
			return err
		}
		out := make(map[string]string, len(arr))
		for _, name := range arr {
			out[name] = AccessLevelAdmin
		}
		*d = out
		return nil
	}
	return fmt.Errorf("deployments must be an object {name:level} or array of names")
}

type APIKey struct {
	ID          int64            `json:"id"`
	KeyID       string           `json:"key_id"`
	UserID      int64            `json:"user_id"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	KeyHash     string           `json:"-"`
	KeyPrefix   string           `json:"key_prefix"`
	Role        Role             `json:"role,omitempty"`
	Permissions []string         `json:"permissions,omitempty"`
	Deployments DeploymentAccess `json:"deployments,omitempty"`
	ExpiresAt   time.Time        `json:"expires_at,omitempty"`
	LastUsedAt  time.Time        `json:"last_used_at,omitempty"`
	LastUsedIP  string           `json:"last_used_ip,omitempty"`
	IsActive    bool             `json:"is_active"`
	CreatedAt   time.Time        `json:"created_at"`
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
	Type        string            `json:"type"`
	UserID      int64             `json:"user_id,omitempty"`
	User        *User             `json:"user,omitempty"`
	APIKey      *APIKey           `json:"api_key,omitempty"`
	Role        Role              `json:"role"`
	Permissions []string          `json:"permissions,omitempty"`
	Deployments map[string]string `json:"deployments,omitempty"`
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
	userLevel := actorUserDeploymentLevel(a, name)
	if userLevel == "" {
		return false
	}

	keyLevel := actorAPIKeyDeploymentLevel(a, name)
	if keyLevel == "" {
		return false
	}

	return accessLevelSufficient(minAccessLevel(userLevel, keyLevel), requiredLevel)
}

func actorUserDeploymentLevel(a *ActorContext, name string) string {
	if a.User != nil && a.User.Role == RoleAdmin {
		return AccessLevelAdmin
	}
	if a.User != nil && a.User.Role == RoleService && a.APIKey != nil {
		return AccessLevelAdmin
	}
	if a.User == nil && a.Role == RoleAdmin {
		return AccessLevelAdmin
	}
	if lvl, ok := a.Deployments[name]; ok {
		return lvl
	}
	return ""
}

func actorAPIKeyDeploymentLevel(a *ActorContext, name string) string {
	if a.APIKey == nil || len(a.APIKey.Deployments) == 0 {
		return AccessLevelAdmin
	}
	if lvl, ok := a.APIKey.Deployments[name]; ok {
		return lvl
	}
	return ""
}

func accessLevelSufficient(has, required string) bool {
	return accessLevelRank(has) >= accessLevelRank(required)
}

func minAccessLevel(a, b string) string {
	if accessLevelRank(a) <= accessLevelRank(b) {
		return a
	}
	return b
}

func accessLevelRank(level string) int {
	switch level {
	case AccessLevelRead:
		return 1
	case AccessLevelWrite:
		return 2
	case AccessLevelAdmin:
		return 3
	}
	return 0
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

func ParseDeploymentsJSON(s string) DeploymentAccess {
	if s == "" {
		return nil
	}
	var d DeploymentAccess
	_ = (&d).UnmarshalJSON([]byte(s))
	return d
}
