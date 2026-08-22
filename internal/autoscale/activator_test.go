package autoscale

import (
	"context"
	"errors"
	"testing"

	"github.com/flatrun/agent/internal/orchestrator"
	"github.com/flatrun/agent/internal/routing"
)

type activationStopper struct {
	stopped bool
	err     error
}

func (s *activationStopper) StopService(_, _ string) (string, error) {
	s.stopped = true
	return "", s.err
}

func TestActivatorCutsOverOnlyAfterManagedReplicasAreReady(t *testing.T) {
	provider := &fakeOrchestrator{status: orchestrator.Status{Workload: "shop", Desired: 2, Available: 2, Instances: []orchestrator.Instance{
		{ID: "one", Address: "10.0.0.1:8080", Healthy: true, Ready: true},
		{ID: "two", Address: "10.0.0.2:8080", Healthy: true, Ready: true},
	}}}
	router := &fakeRouter{}
	stopper := &activationStopper{}
	activation, err := NewActivator(provider, router, stopper).Activate(context.Background(), "shop", "web", orchestrator.Workload{ID: "shop", Image: "shop:1", Replicas: 2}, routing.Route{ID: "shop", Service: "web", Domain: "shop.example.com", Protocol: "http"})
	if err != nil {
		t.Fatal(err)
	}
	if !stopper.stopped || len(router.reconciled.Backends) != 2 || activation.Workload.Available != 2 {
		t.Fatalf("activation = %#v, route = %#v", activation, router.reconciled)
	}
}

func TestActivatorRollsBackWhenComposeCannotStop(t *testing.T) {
	provider := &fakeOrchestrator{status: orchestrator.Status{Workload: "shop", Desired: 1, Available: 1, Instances: []orchestrator.Instance{{ID: "one", Address: "10.0.0.1:8080", Healthy: true, Ready: true}}}}
	router := &fakeRouter{}
	stopper := &activationStopper{err: errors.New("compose failed")}
	_, err := NewActivator(provider, router, stopper).Activate(context.Background(), "shop", "web", orchestrator.Workload{ID: "shop", Image: "shop:1", Replicas: 1}, routing.Route{ID: "shop", Service: "web", Domain: "shop.example.com", Protocol: "http"})
	if err == nil {
		t.Fatal("activation succeeded")
	}
	if !stopper.stopped {
		t.Fatal("Compose stop was not attempted")
	}
	if provider.removed != "shop" || router.removed != "shop" {
		t.Fatalf("rollback removed workload %q and route %q", provider.removed, router.removed)
	}
}
