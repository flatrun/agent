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
