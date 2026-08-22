package routing

import (
	"context"
	"strings"
	"testing"

	"github.com/flatrun/agent/internal/nginx"
	"github.com/flatrun/agent/pkg/models"
)

type managedNginxRecorder struct {
	backends map[string][]nginx.UpstreamBackend
	content  string
	reloads  int
}

func (m *managedNginxRecorder) RenderVirtualHost(_ *models.Deployment) (string, error) {
	return "compose deployment config", nil
}

func (m *managedNginxRecorder) RenderVirtualHostWithBackends(_ *models.Deployment, backends map[string][]nginx.UpstreamBackend) (string, error) {
	m.backends = backends
	return "preserved deployment config", nil
}
func (m *managedNginxRecorder) GetVirtualHost(string) (string, error) { return "previous config", nil }
func (m *managedNginxRecorder) WriteVirtualHost(_, content string) error {
	m.content = content
	return nil
}
func (m *managedNginxRecorder) TestConfig() error { return nil }
func (m *managedNginxRecorder) Reload() error     { m.reloads++; return nil }

type deploymentSourceStub struct{ deployment *models.Deployment }

func (s deploymentSourceStub) GetDeployment(string) (*models.Deployment, error) {
	return s.deployment, nil
}

func TestManagedNginxProviderReconcilesAndDrainsDeploymentBackends(t *testing.T) {
	manager := &managedNginxRecorder{}
	provider := NewManagedNginxProvider(manager, deploymentSourceStub{deployment: &models.Deployment{Name: "shop"}})
	route := Route{ID: "shop", Service: "web", Domain: "shop.example.com", Protocol: "http", Backends: []Backend{
		{ID: "one", Address: "10.42.0.8:8080", Healthy: true, Weight: 1},
		{ID: "two", Address: "10.42.1.9:8080", Healthy: true, Weight: 1},
	}}
	if err := provider.Reconcile(context.Background(), route); err != nil {
		t.Fatal(err)
	}
	if manager.content != "preserved deployment config" || manager.reloads != 1 || len(manager.backends["web:8080"]) != 2 {
		t.Fatalf("manager = %#v", manager)
	}
	if err := provider.Drain(context.Background(), "shop", "two"); err != nil {
		t.Fatal(err)
	}
	if manager.backends["web:8080"][1].Healthy || manager.reloads != 2 {
		t.Fatalf("drained backends = %#v", manager.backends["web:8080"])
	}
}

func TestManagedNginxProviderRequiresDeploymentService(t *testing.T) {
	provider := NewManagedNginxProvider(&managedNginxRecorder{}, deploymentSourceStub{deployment: &models.Deployment{Name: "shop"}})
	err := provider.Validate(context.Background(), Route{ID: "shop", Domain: "shop.example.com", Protocol: "http", Backends: []Backend{{ID: "one", Address: "10.0.0.1:80", Healthy: true}}})
	if err == nil || !strings.Contains(err.Error(), "service") {
		t.Fatalf("error = %v", err)
	}
}

func TestManagedNginxProviderRestoresComposeRouteOnRemove(t *testing.T) {
	manager := &managedNginxRecorder{}
	provider := NewManagedNginxProvider(manager, deploymentSourceStub{deployment: &models.Deployment{Name: "shop"}})
	if err := provider.Remove(context.Background(), "shop"); err != nil {
		t.Fatal(err)
	}
	if manager.content != "compose deployment config" || manager.reloads != 1 {
		t.Fatalf("manager = %#v", manager)
	}
}

func TestManagedNginxProviderRestoresRouteWithoutReloading(t *testing.T) {
	manager := &managedNginxRecorder{}
	route := Route{ID: "shop", Service: "web", Domain: "shop.example.com", Protocol: "http", Backends: []Backend{{ID: "one", Address: "10.42.0.8:8080", Healthy: true}}}
	provider := NewManagedNginxProvider(manager, deploymentSourceStub{deployment: &models.Deployment{Name: "shop"}}, route)

	if manager.reloads != 0 {
		t.Fatalf("reloads = %d", manager.reloads)
	}
	if err := provider.Drain(context.Background(), "shop", "one"); err != nil {
		t.Fatal(err)
	}
	if manager.reloads != 1 || manager.backends["web:8080"][0].Healthy {
		t.Fatalf("manager = %#v", manager)
	}
}

func TestManagedNginxProviderOwnsReturnedBackendSlice(t *testing.T) {
	route := Route{ID: "shop", Backends: []Backend{{ID: "one", Healthy: true}}}
	provider := NewManagedNginxProvider(&managedNginxRecorder{}, deploymentSourceStub{}, route).(*managedNginxProvider)

	loaded, ok := provider.route("shop")
	if !ok {
		t.Fatal("route was not restored")
	}
	loaded.Backends[0].Healthy = false
	stored, _ := provider.route("shop")
	if !stored.Backends[0].Healthy {
		t.Fatal("caller mutation changed the stored route")
	}
}
