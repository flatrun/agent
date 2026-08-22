package routing

import "context"

type ProviderID string

const (
	ProviderNginx   ProviderID = "nginx"
	ProviderTraefik ProviderID = "traefik"
)

type Backend struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Healthy bool   `json:"healthy"`
	Weight  int    `json:"weight,omitempty"`
}

type Route struct {
	ID       string    `json:"id"`
	Service  string    `json:"service,omitempty"`
	Domain   string    `json:"domain"`
	Path     string    `json:"path,omitempty"`
	Protocol string    `json:"protocol"`
	Backends []Backend `json:"backends"`
}

type Provider interface {
	ID() ProviderID
	Validate(context.Context, Route) error
	Reconcile(context.Context, Route) error
	Drain(context.Context, string, string) error
	Remove(context.Context, string) error
}
