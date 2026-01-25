package models

import "time"

type InfraService struct {
	Name        string         `json:"name"`
	Type        string         `json:"type"`
	Status      string         `json:"status"`
	Managed     bool           `json:"managed"`
	External    bool           `json:"external"`
	ContainerID string         `json:"container_id,omitempty"`
	Image       string         `json:"image,omitempty"`
	Health      string         `json:"health,omitempty"`
	CreatedAt   time.Time      `json:"created_at,omitempty"`
	Config      map[string]any `json:"config,omitempty"`
}

const (
	InfraTypeNginx    = "nginx"
	InfraTypeCertbot  = "certbot"
	InfraTypeDatabase = "database"
	InfraTypeRedis    = "redis"
	InfraTypePowerDNS = "powerdns"

	InfraStatusRunning  = "running"
	InfraStatusStopped  = "stopped"
	InfraStatusExternal = "external"
	InfraStatusUnknown  = "unknown"
)
