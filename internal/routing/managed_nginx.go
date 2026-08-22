package routing

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/flatrun/agent/internal/nginx"
	"github.com/flatrun/agent/pkg/models"
)

type DeploymentSource interface {
	GetDeployment(string) (*models.Deployment, error)
}

type ManagedNginx interface {
	RenderVirtualHost(*models.Deployment) (string, error)
	RenderVirtualHostWithBackends(*models.Deployment, map[string][]nginx.UpstreamBackend) (string, error)
	GetVirtualHost(string) (string, error)
	WriteVirtualHost(string, string) error
	TestConfig() error
	Reload() error
}

type managedNginxProvider struct {
	manager     ManagedNginx
	deployments DeploymentSource
	mu          sync.RWMutex
	routes      map[string]Route
}

func NewManagedNginxProvider(manager ManagedNginx, deployments DeploymentSource, restored ...Route) Provider {
	routes := make(map[string]Route, len(restored))
	for _, route := range restored {
		routes[route.ID] = cloneRoute(route)
	}
	return &managedNginxProvider{manager: manager, deployments: deployments, routes: routes}
}

func (p *managedNginxProvider) ID() ProviderID { return ProviderNginx }

func (p *managedNginxProvider) Validate(_ context.Context, route Route) error {
	if route.Service == "" {
		return fmt.Errorf("Route service is required")
	}
	return validateRoute(route)
}

func (p *managedNginxProvider) Reconcile(ctx context.Context, route Route) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.Validate(ctx, route); err != nil {
		return err
	}
	deployment, err := p.deployments.GetDeployment(route.ID)
	if err != nil {
		return fmt.Errorf("load deployment for route: %w", err)
	}
	backends := make([]nginx.UpstreamBackend, 0, len(route.Backends))
	for _, backend := range route.Backends {
		backends = append(backends, nginx.UpstreamBackend{Address: backend.Address, Healthy: backend.Healthy, Weight: backend.Weight})
	}
	_, port, err := net.SplitHostPort(route.Backends[0].Address)
	if err != nil {
		return fmt.Errorf("resolve managed backend port: %w", err)
	}
	content, err := p.manager.RenderVirtualHostWithBackends(deployment, map[string][]nginx.UpstreamBackend{route.Service + ":" + port: backends})
	if err != nil {
		return fmt.Errorf("render deployment route: %w", err)
	}
	previousContent, previousErr := p.manager.GetVirtualHost(route.ID)
	if err := p.manager.WriteVirtualHost(route.ID, content); err != nil {
		return fmt.Errorf("write deployment route: %w", err)
	}
	if err := p.manager.TestConfig(); err != nil {
		if previousErr == nil {
			_ = p.manager.WriteVirtualHost(route.ID, previousContent)
		}
		return fmt.Errorf("test Nginx configuration: %w", err)
	}
	if err := p.manager.Reload(); err != nil {
		return fmt.Errorf("reload Nginx: %w", err)
	}
	p.mu.Lock()
	p.routes[route.ID] = cloneRoute(route)
	p.mu.Unlock()
	return nil
}

func cloneRoute(route Route) Route {
	route.Backends = append([]Backend(nil), route.Backends...)
	return route
}

func (p *managedNginxProvider) Drain(ctx context.Context, routeID, backendID string) error {
	route, ok := p.route(routeID)
	if !ok {
		return fmt.Errorf("Route %q is not managed", routeID)
	}
	found := false
	for index := range route.Backends {
		if route.Backends[index].ID == backendID {
			route.Backends[index].Healthy = false
			found = true
		}
	}
	if !found {
		return fmt.Errorf("Backend %q is not part of route %q", backendID, routeID)
	}
	return p.Reconcile(ctx, route)
}

func (p *managedNginxProvider) Remove(ctx context.Context, routeID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deployment, err := p.deployments.GetDeployment(routeID)
	if err != nil {
		return fmt.Errorf("load deployment for route: %w", err)
	}
	content, err := p.manager.RenderVirtualHost(deployment)
	if err != nil {
		return fmt.Errorf("render deployment route: %w", err)
	}
	if err := p.manager.WriteVirtualHost(routeID, content); err != nil {
		return fmt.Errorf("write deployment route: %w", err)
	}
	if err := p.manager.TestConfig(); err != nil {
		return fmt.Errorf("test Nginx configuration: %w", err)
	}
	if err := p.manager.Reload(); err != nil {
		return fmt.Errorf("reload Nginx: %w", err)
	}
	p.mu.Lock()
	delete(p.routes, routeID)
	p.mu.Unlock()
	return nil
}

func (p *managedNginxProvider) route(id string) (Route, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	route, ok := p.routes[id]
	return cloneRoute(route), ok
}
