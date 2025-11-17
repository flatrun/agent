package models

import "time"

type Deployment struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Services  []Service `json:"services,omitempty"`
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
	Name string `yaml:"name" json:"name"`
	Type string `yaml:"type" json:"type"`
	Networking NetworkingConfig `yaml:"networking" json:"networking"`
	SSL SSLConfig `yaml:"ssl" json:"ssl"`
	HealthCheck HealthCheckConfig `yaml:"healthcheck" json:"healthcheck"`
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
