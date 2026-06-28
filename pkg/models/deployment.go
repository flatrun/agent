package models

import "time"

type Deployment struct {
	Name      string           `json:"name"`
	Path      string           `json:"path"`
	Status    string           `json:"status"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
	Services  []Service        `json:"services,omitempty"`
	Metadata  *ServiceMetadata `json:"metadata,omitempty"`
}

type Service struct {
	Name        string    `json:"name"`
	ContainerID string    `json:"container_id"`
	Image       string    `json:"image"`
	Status      string    `json:"status"`
	Health      string    `json:"health,omitempty"`
	Ports       []string  `json:"ports,omitempty"`
	Networks    []string  `json:"networks,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type ServiceMetadata struct {
	Name               string                    `yaml:"name" json:"name"`
	Type               string                    `yaml:"type" json:"type"`
	Networking         NetworkingConfig          `yaml:"networking" json:"networking"`
	SSL                SSLConfig                 `yaml:"ssl" json:"ssl"`
	HealthCheck        HealthCheckConfig         `yaml:"healthcheck" json:"healthcheck"`
	QuickActions       []QuickAction             `yaml:"quick_actions,omitempty" json:"quick_actions,omitempty"`
	Security           *DeploymentSecurityConfig `yaml:"security,omitempty" json:"security,omitempty"`
	Backup             *BackupSpec               `yaml:"backup,omitempty" json:"backup,omitempty"`
	ProtectedMode      *ProtectedModeConfig      `yaml:"protected_mode,omitempty" json:"protected_mode,omitempty"`
	RequirePlan        bool                      `yaml:"require_plan,omitempty" json:"require_plan,omitempty"`
	CredentialID       string                    `yaml:"credential_id,omitempty" json:"credential_id,omitempty"`
	ServiceCredentials map[string]string         `yaml:"service_credentials,omitempty" json:"service_credentials,omitempty"`
	Domains            []DomainConfig            `yaml:"domains,omitempty" json:"domains,omitempty"`
	Databases          []DatabaseConfig          `yaml:"databases,omitempty" json:"databases,omitempty"`
}

type DomainConfig struct {
	ID            string    `yaml:"id" json:"id"`
	Service       string    `yaml:"service" json:"service"`
	ContainerPort int       `yaml:"container_port" json:"container_port"`
	Domain        string    `yaml:"domain" json:"domain"`
	PathPrefix    string    `yaml:"path_prefix,omitempty" json:"path_prefix,omitempty"`
	StripPrefix   bool      `yaml:"strip_prefix,omitempty" json:"strip_prefix,omitempty"`
	SSL           SSLConfig `yaml:"ssl" json:"ssl"`
	Aliases       []string  `yaml:"aliases,omitempty" json:"aliases,omitempty"`
	// ProxyTimeout is the proxy read/send timeout in seconds. Defaults to 60
	// when unset; raise it for domains that proxy long-lived WebSocket
	// connections so idle sockets are not closed mid-connection.
	ProxyTimeout int `yaml:"proxy_timeout,omitempty" json:"proxy_timeout,omitempty"`
}

type DatabaseConfig struct {
	ID           string `yaml:"id" json:"id"`
	Alias        string `yaml:"alias" json:"alias"`
	Type         string `yaml:"type" json:"type"`
	Mode         string `yaml:"mode" json:"mode"`
	Service      string `yaml:"service,omitempty" json:"service,omitempty"`
	Host         string `yaml:"host,omitempty" json:"host,omitempty"`
	Port         int    `yaml:"port,omitempty" json:"port,omitempty"`
	Container    string `yaml:"container,omitempty" json:"container,omitempty"`
	DatabaseName string `yaml:"database_name,omitempty" json:"database_name,omitempty"`
	Username     string `yaml:"username,omitempty" json:"username,omitempty"`
	EnvPrefix    string `yaml:"env_prefix,omitempty" json:"env_prefix,omitempty"`
	IsShared     bool   `yaml:"is_shared,omitempty" json:"is_shared,omitempty"`
}

func (m *ServiceMetadata) GetDomains() []DomainConfig {
	if len(m.Domains) > 0 {
		return m.Domains
	}
	if !m.Networking.Expose || m.Networking.Domain == "" {
		return nil
	}
	service := m.Networking.Service
	if service == "" {
		service = m.Name
	}
	return []DomainConfig{{
		ID:            "default",
		Service:       service,
		ContainerPort: m.Networking.ContainerPort,
		Domain:        m.Networking.Domain,
		SSL:           m.SSL,
	}}
}

func (m *ServiceMetadata) GetUniqueDomainNames() []string {
	domains := m.GetDomains()
	domainSet := make(map[string]struct{})
	for _, d := range domains {
		domainSet[d.Domain] = struct{}{}
		for _, alias := range d.Aliases {
			domainSet[alias] = struct{}{}
		}
	}
	result := make([]string, 0, len(domainSet))
	for name := range domainSet {
		result = append(result, name)
	}
	return result
}

func (m *ServiceMetadata) HasMultipleDomains() bool {
	return len(m.Domains) > 1
}

func (m *ServiceMetadata) GetDatabases() []DatabaseConfig {
	return m.Databases
}

func (m *ServiceMetadata) GetPrimaryDatabase() *DatabaseConfig {
	if len(m.Databases) == 0 {
		return nil
	}
	for i := range m.Databases {
		if m.Databases[i].Alias == "primary" {
			return &m.Databases[i]
		}
	}
	return &m.Databases[0]
}

func (m *ServiceMetadata) HasMultipleDatabases() bool {
	return len(m.Databases) > 1
}

type BackupSpec struct {
	ContainerPaths  []ContainerBackupPath `yaml:"container_paths,omitempty" json:"container_paths,omitempty"`
	Databases       []DatabaseBackupSpec  `yaml:"databases,omitempty" json:"databases,omitempty"`
	PreHooks        []BackupHookSpec      `yaml:"pre_hooks,omitempty" json:"pre_hooks,omitempty"`
	PostHooks       []BackupHookSpec      `yaml:"post_hooks,omitempty" json:"post_hooks,omitempty"`
	ExcludePatterns []string              `yaml:"exclude_patterns,omitempty" json:"exclude_patterns,omitempty"`
}

type ContainerBackupPath struct {
	Service       string `yaml:"service" json:"service"`
	ContainerPath string `yaml:"container_path" json:"container_path"`
	Description   string `yaml:"description,omitempty" json:"description,omitempty"`
	Required      bool   `yaml:"required" json:"required"`
}

type DatabaseBackupSpec struct {
	Service     string `yaml:"service" json:"service"`
	Type        string `yaml:"type" json:"type"`
	HostEnv     string `yaml:"host_env,omitempty" json:"host_env,omitempty"`
	PortEnv     string `yaml:"port_env,omitempty" json:"port_env,omitempty"`
	UserEnv     string `yaml:"user_env,omitempty" json:"user_env,omitempty"`
	PasswordEnv string `yaml:"password_env,omitempty" json:"password_env,omitempty"`
	DatabaseEnv string `yaml:"database_env,omitempty" json:"database_env,omitempty"`
	Host        string `yaml:"host,omitempty" json:"host,omitempty"`
	Port        int    `yaml:"port,omitempty" json:"port,omitempty"`
	User        string `yaml:"user,omitempty" json:"user,omitempty"`
	Password    string `yaml:"password,omitempty" json:"password,omitempty"`
	Database    string `yaml:"database,omitempty" json:"database,omitempty"`
}

type BackupHookSpec struct {
	Service string `yaml:"service" json:"service"`
	Command string `yaml:"command" json:"command"`
	Timeout int    `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

type QuickAction struct {
	ID          string `yaml:"id" json:"id"`
	Name        string `yaml:"name" json:"name"`
	Command     string `yaml:"command" json:"command"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Icon        string `yaml:"icon,omitempty" json:"icon,omitempty"`
	Service     string `yaml:"service,omitempty" json:"service,omitempty"`
}

type ProtectedModeConfig struct {
	Enabled             bool                   `yaml:"enabled" json:"enabled"`
	BlockedActions      []string               `yaml:"blocked_actions,omitempty" json:"blocked_actions,omitempty"`
	BlockedCommandRules []ProtectedCommandRule `yaml:"blocked_command_rules,omitempty" json:"blocked_command_rules,omitempty"`
	DisableTerminal     bool                   `yaml:"disable_terminal,omitempty" json:"disable_terminal,omitempty"`
}

type ProtectedCommandRule struct {
	ID            string `yaml:"id,omitempty" json:"id,omitempty"`
	Name          string `yaml:"name,omitempty" json:"name,omitempty"`
	Match         string `yaml:"match" json:"match"`
	Pattern       string `yaml:"pattern" json:"pattern"`
	CaseSensitive bool   `yaml:"case_sensitive,omitempty" json:"case_sensitive,omitempty"`
	Description   string `yaml:"description,omitempty" json:"description,omitempty"`
}

type NetworkingConfig struct {
	Expose        bool   `yaml:"expose" json:"expose"`
	Domain        string `yaml:"domain" json:"domain"`
	Service       string `yaml:"service,omitempty" json:"service,omitempty"`
	ContainerPort int    `yaml:"container_port" json:"container_port"`
	Protocol      string `yaml:"protocol" json:"protocol"`
	ProxyType     string `yaml:"proxy_type" json:"proxy_type"`
}

type SSLConfig struct {
	Enabled  bool `yaml:"enabled" json:"enabled"`
	AutoCert bool `yaml:"auto_cert" json:"auto_cert"`
}

type HealthCheckConfig struct {
	Path     string `yaml:"path" json:"path"`
	Interval string `yaml:"interval" json:"interval"`
}

type DeploymentStatus string

const (
	StatusRunning DeploymentStatus = "running"
	StatusStopped DeploymentStatus = "stopped"
	StatusError   DeploymentStatus = "error"
	StatusUnknown DeploymentStatus = "unknown"
)

type DeploymentSecurityConfig struct {
	Enabled        bool                  `yaml:"enabled" json:"enabled"`
	BlockedIPs     []string              `yaml:"blocked_ips,omitempty" json:"blocked_ips,omitempty"`
	ProtectedPaths []ProtectedPath       `yaml:"protected_paths,omitempty" json:"protected_paths,omitempty"`
	RateLimits     []DeploymentRateLimit `yaml:"rate_limits,omitempty" json:"rate_limits,omitempty"`
}

type ProtectedPath struct {
	Pattern string `yaml:"pattern" json:"pattern"`
	Enabled bool   `yaml:"enabled" json:"enabled"`
}

type DeploymentRateLimit struct {
	Path    string `yaml:"path" json:"path"`
	Rate    int    `yaml:"rate" json:"rate"`
	Burst   int    `yaml:"burst" json:"burst"`
	Enabled bool   `yaml:"enabled" json:"enabled"`
}
