package autoscale

import (
	"context"
	"fmt"
	"time"

	"github.com/flatrun/agent/internal/events"
	"github.com/flatrun/agent/internal/routing"
)

type PolicyStore interface {
	Policy(string) (Policy, error)
	State(string) (State, error)
	SetState(string, State) error
}

type ActionExecutor interface {
	Execute(context.Context, string, routing.Route, Decision) (Execution, error)
}

type EventPublisher interface {
	Publish(events.Event) (events.IngestResult, error)
}

type Runner struct {
	store     PolicyStore
	executor  ActionExecutor
	publisher EventPublisher
	node      string
}

type ReconcileResult struct {
	State     State      `json:"state"`
	Decision  Decision   `json:"decision"`
	Execution *Execution `json:"execution,omitempty"`
}

func NewRunner(store PolicyStore, executor ActionExecutor, publisher EventPublisher, node string) *Runner {
	return &Runner{store: store, executor: executor, publisher: publisher, node: node}
}

func (r *Runner) Reconcile(ctx context.Context, deployment string, input Input, route routing.Route) (ReconcileResult, error) {
	policy, err := r.store.Policy(deployment)
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("load autoscaling policy: %w", err)
	}
	state, err := r.store.State(deployment)
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("load autoscaling state: %w", err)
	}
	nextState, decision := Reconcile(policy, state, input)
	if err := r.store.SetState(deployment, nextState); err != nil {
		return ReconcileResult{}, fmt.Errorf("save autoscaling state: %w", err)
	}
	result := ReconcileResult{State: nextState, Decision: decision}
	if decision.Action == ActionNone {
		return result, nil
	}
	if decision.Action == ActionNotify {
		r.publishFailure(deployment, decision.Reason, events.SeverityWarning)
		return result, nil
	}
	execution, err := r.executor.Execute(ctx, deployment, route, decision)
	result.Execution = &execution
	if err != nil {
		r.publishFailure(deployment, err.Error(), events.SeverityCritical)
		return result, fmt.Errorf("execute autoscaling decision: %w", err)
	}
	if execution.Pending {
		return result, nil
	}
	nextState.Replicas = execution.Status.Desired
	if execution.Route.ID != "" {
		nextState.Route = execution.Route
	}
	if err := r.store.SetState(deployment, nextState); err != nil {
		return result, fmt.Errorf("save autoscaling execution: %w", err)
	}
	result.State = nextState
	return result, nil
}

func (r *Runner) publishFailure(deployment, message string, severity events.Severity) {
	if r.publisher == nil {
		return
	}
	_, _ = r.publisher.Publish(events.Event{
		Source: "capacity", Type: "autoscale.blocked", Severity: severity,
		Title: "Autoscaling needs attention", Message: message,
		Scope:          events.Scope{Node: r.node, Deployment: deployment},
		CorrelationKey: "autoscale:" + r.node + ":" + deployment, OccurredAt: time.Now(),
	})
}
