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
	Name         string                    `yaml:"name" json:"name"`
	Type         string                    `yaml:"type" json:"type"`
	Networking   NetworkingConfig          `yaml:"networking" json:"networking"`
	SSL          SSLConfig                 `yaml:"ssl" json:"ssl"`
	HealthCheck  HealthCheckConfig         `yaml:"healthcheck" json:"healthcheck"`
	QuickActions []QuickAction             `yaml:"quick_actions,omitempty" json:"quick_actions,omitempty"`
	Security     *DeploymentSecurityConfig `yaml:"security,omitempty" json:"security,omitempty"`
}

type QuickAction struct {
	ID          string `yaml:"id" json:"id"`
	Name        string `yaml:"name" json:"name"`
	Command     string `yaml:"command" json:"command"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Icon        string `yaml:"icon,omitempty" json:"icon,omitempty"`
	Service     string `yaml:"service,omitempty" json:"service,omitempty"`
}

type NetworkingConfig struct {
	Expose        bool   `yaml:"expose" json:"expose"`
	Domain        string `yaml:"domain" json:"domain"`
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
