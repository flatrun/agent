package autoscale

import (
	"context"
	"testing"

	"github.com/flatrun/agent/internal/orchestrator"
	"github.com/flatrun/agent/internal/routing"
)

type fakeOrchestrator struct {
	status      orchestrator.Status
	scaledTo    int
	resizedWith orchestrator.Resources
}

func (f *fakeOrchestrator) ID() orchestrator.ProviderID                           { return orchestrator.ProviderSwarm }
func (f *fakeOrchestrator) Validate(context.Context, orchestrator.Workload) error { return nil }
func (f *fakeOrchestrator) Apply(context.Context, orchestrator.Workload) (orchestrator.Status, error) {
	return f.status, nil
}
func (f *fakeOrchestrator) Resize(_ context.Context, _ string, resources orchestrator.Resources) (orchestrator.Status, error) {
	f.resizedWith = resources
	return f.status, nil
}
func (f *fakeOrchestrator) Scale(_ context.Context, _ string, replicas int) (orchestrator.Status, error) {
	f.scaledTo = replicas
	f.status.Desired = replicas
	return f.status, nil
}
func (f *fakeOrchestrator) Status(context.Context, string) (orchestrator.Status, error) {
	return f.status, nil
}
func (f *fakeOrchestrator) Remove(context.Context, string) error { return nil }

type fakeRouter struct {
	reconciled routing.Route
	drained    string
}

func (f *fakeRouter) ID() routing.ProviderID                        { return routing.ProviderNginx }
func (f *fakeRouter) Validate(context.Context, routing.Route) error { return nil }
func (f *fakeRouter) Reconcile(_ context.Context, route routing.Route) error {
	f.reconciled = route
	return nil
}
func (f *fakeRouter) Drain(_ context.Context, _, backendID string) error {
	f.drained = backendID
	return nil
}
func (f *fakeRouter) Remove(context.Context, string) error { return nil }

func TestExecutorPublishesOnlyReadyScaledReplicas(t *testing.T) {
	orchestratorProvider := &fakeOrchestrator{status: orchestrator.Status{
		Workload: "shop", Desired: 1, Available: 2,
		Instances: []orchestrator.Instance{
			{ID: "one", Address: "10.0.0.1:8080", Healthy: true, Ready: true},
			{ID: "two", Address: "10.0.0.2:8080", Healthy: true, Ready: true},
		},
	}}
	router := &fakeRouter{}
	execution, err := NewExecutor(orchestratorProvider, router).Execute(
		context.Background(),
		"shop",
		routing.Route{ID: "shop", Domain: "shop.example.com", Protocol: "http"},
		Decision{Action: ActionAddReplica, Replicas: 2},
	)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if execution.Pending || orchestratorProvider.scaledTo != 2 || len(router.reconciled.Backends) != 2 {
		t.Fatalf("execution = %#v, route = %#v", execution, router.reconciled)
	}
}

func TestExecutorWaitsForNewReplicaBeforeRouting(t *testing.T) {
	orchestratorProvider := &fakeOrchestrator{status: orchestrator.Status{
		Workload: "shop", Desired: 1, Available: 1,
		Instances: []orchestrator.Instance{{ID: "one", Address: "10.0.0.1:8080", Healthy: true, Ready: true}},
	}}
	router := &fakeRouter{}
	execution, err := NewExecutor(orchestratorProvider, router).Execute(
		context.Background(), "shop", routing.Route{ID: "shop"}, Decision{Action: ActionAddReplica, Replicas: 2},
	)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !execution.Pending || len(router.reconciled.Backends) != 0 {
		t.Fatalf("execution = %#v, route = %#v", execution, router.reconciled)
	}
}

func TestExecutorDrainsBeforeRemovingReplica(t *testing.T) {
	orchestratorProvider := &fakeOrchestrator{status: orchestrator.Status{
		Workload: "shop", Desired: 2, Available: 2,
		Instances: []orchestrator.Instance{{ID: "one", Ready: true}, {ID: "two", Ready: true}},
	}}
	router := &fakeRouter{}
	_, err := NewExecutor(orchestratorProvider, router).Execute(
		context.Background(),
		"shop",
		routing.Route{ID: "shop", Backends: []routing.Backend{{ID: "one"}, {ID: "two"}}},
		Decision{Action: ActionRemoveReplica, Replicas: 1},
	)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if router.drained != "two" || orchestratorProvider.scaledTo != 1 {
		t.Fatalf("drained = %q, scaled = %d", router.drained, orchestratorProvider.scaledTo)
	}
}
