package autoscale

import (
	"context"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/capacity"
	"github.com/flatrun/agent/internal/events"
	"github.com/flatrun/agent/internal/orchestrator"
	"github.com/flatrun/agent/internal/routing"
)

type runnerStore struct {
	policy Policy
	state  State
}

func (s *runnerStore) Policy(string) (Policy, error)        { return s.policy, nil }
func (s *runnerStore) State(string) (State, error)          { return s.state, nil }
func (s *runnerStore) SetState(_ string, state State) error { s.state = state; return nil }

type runnerExecutor struct {
	decision Decision
}

func (e *runnerExecutor) Execute(_ context.Context, _ string, _ routing.Route, decision Decision) (Execution, error) {
	e.decision = decision
	return Execution{Decision: decision, Status: orchestrator.Status{Desired: decision.Replicas}}, nil
}

type runnerPublisher struct {
	events []events.Event
}

func (p *runnerPublisher) Publish(event events.Event) (events.IngestResult, error) {
	p.events = append(p.events, event)
	return events.IngestResult{}, nil
}

func TestRunnerPersistsObservationAndExecutesDecision(t *testing.T) {
	policy := DefaultPolicy()
	policy.ScaleUpWindows = 1
	store := &runnerStore{policy: policy}
	executor := &runnerExecutor{}
	runner := NewRunner(store, executor, nil, "prod-1")
	result, err := runner.Reconcile(context.Background(), "shop", Input{
		Now: time.Now(), Replicas: 1, CPUPercent: 90,
		Diagnosis: capacity.Diagnosis{Action: capacity.ActionAddReplica},
	}, routing.Route{ID: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision.Action != ActionAddReplica || executor.decision.Replicas != 2 || store.state.HighWindows != 0 {
		t.Fatalf("unexpected reconciliation: %+v", result)
	}
}

func TestRunnerPublishesCorrelatedBlockedEvent(t *testing.T) {
	policy := DefaultPolicy()
	policy.MaxReplicas = 1
	policy.ScaleUpWindows = 1
	store := &runnerStore{policy: policy}
	publisher := &runnerPublisher{}
	runner := NewRunner(store, &runnerExecutor{}, publisher, "prod-1")
	_, err := runner.Reconcile(context.Background(), "shop", Input{Now: time.Now(), Replicas: 1, CPUPercent: 95}, routing.Route{ID: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if len(publisher.events) != 1 || publisher.events[0].CorrelationKey != "autoscale:prod-1:shop" {
		t.Fatalf("unexpected events: %+v", publisher.events)
	}
}

func TestRunnerPersistsSuccessfulExecution(t *testing.T) {
	policy := DefaultPolicy()
	policy.ScaleUpWindows = 1
	store := &runnerStore{policy: policy, state: State{Active: true, Replicas: 1}}
	provider := &fakeOrchestrator{status: orchestrator.Status{
		Workload: "shop", Desired: 1, Available: 2,
		Instances: []orchestrator.Instance{
			{ID: "one", Address: "10.0.0.1:8080", Healthy: true, Ready: true},
			{ID: "two", Address: "10.0.0.2:8080", Healthy: true, Ready: true},
		},
	}}
	router := &fakeRouter{}
	runner := NewRunner(store, NewExecutor(provider, router), nil, "prod-1")
	result, err := runner.Reconcile(context.Background(), "shop", Input{Now: time.Now(), Replicas: 1, CPUPercent: 95}, routing.Route{ID: "shop", Service: "web", Domain: "shop.example.com", Protocol: "http"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State.Replicas != 2 || len(result.State.Route.Backends) != 2 {
		t.Fatalf("state = %#v", result.State)
	}
}

func TestRunnerPublishesPendingReplicaWhenItBecomesReady(t *testing.T) {
	policy := DefaultPolicy()
	policy.ScaleUpWindows = 1
	now := time.Now()
	store := &runnerStore{policy: policy, state: State{Active: true, Replicas: 1}}
	provider := &fakeOrchestrator{status: orchestrator.Status{
		Workload: "shop", Desired: 1, Available: 1,
		Instances: []orchestrator.Instance{{ID: "one", Address: "10.0.0.1:8080", Healthy: true, Ready: true}},
	}}
	router := &fakeRouter{}
	runner := NewRunner(store, NewExecutor(provider, router), nil, "prod-1")
	route := routing.Route{ID: "shop", Service: "web", Domain: "shop.example.com", Protocol: "http", Backends: []routing.Backend{{ID: "one", Address: "10.0.0.1:8080", Healthy: true, Weight: 1}}}
	result, err := runner.Reconcile(context.Background(), "shop", Input{Now: now, Replicas: 1, CPUPercent: 95}, route)
	if err != nil || result.Execution == nil || !result.Execution.Pending || store.state.Replicas != 2 {
		t.Fatalf("result = %#v, state = %#v, error = %v", result, store.state, err)
	}
	provider.status.Available = 2
	provider.status.Instances = append(provider.status.Instances, orchestrator.Instance{ID: "two", Address: "10.0.0.2:8080", Healthy: true, Ready: true})
	result, err = runner.Reconcile(context.Background(), "shop", Input{Now: now.Add(time.Second), Replicas: 2}, route)
	if err != nil || result.Execution == nil || result.Execution.Pending || len(store.state.Route.Backends) != 2 {
		t.Fatalf("result = %#v, state = %#v, error = %v", result, store.state, err)
	}
}
