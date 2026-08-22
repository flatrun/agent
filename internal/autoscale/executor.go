package autoscale

import (
	"context"
	"fmt"
	"slices"

	"github.com/flatrun/agent/internal/orchestrator"
	"github.com/flatrun/agent/internal/routing"
)

type Executor struct {
	orchestrator orchestrator.Provider
	routing      routing.Provider
}

type Execution struct {
	Decision Decision            `json:"decision"`
	Status   orchestrator.Status `json:"status"`
	Route    routing.Route       `json:"route,omitempty"`
	Pending  bool                `json:"pending"`
}

func NewExecutor(orchestrator orchestrator.Provider, routing routing.Provider) *Executor {
	return &Executor{orchestrator: orchestrator, routing: routing}
}

func (e *Executor) Execute(ctx context.Context, workloadID string, route routing.Route, decision Decision) (Execution, error) {
	result := Execution{Decision: decision}
	switch decision.Action {
	case ActionNone:
		status, err := e.orchestrator.Status(ctx, workloadID)
		result.Status = status
		if err != nil || status.Available < status.Desired {
			result.Pending = err == nil
			return result, err
		}
		updated, err := routeWithReadyInstances(route, status)
		if err != nil {
			return result, err
		}
		if slices.Equal(updated.Backends, route.Backends) {
			return result, nil
		}
		if err := e.routing.Reconcile(ctx, updated); err != nil {
			return result, fmt.Errorf("publish ready route: %w", err)
		}
		result.Route = updated
		return result, nil
	case ActionNotify:
		status, err := e.orchestrator.Status(ctx, workloadID)
		result.Status = status
		return result, err
	case ActionIncreaseCPU, ActionIncreaseMemory:
		status, err := e.orchestrator.Resize(ctx, workloadID, decision.Resources)
		result.Status = status
		return result, err
	case ActionAddReplica:
		status, err := e.orchestrator.Scale(ctx, workloadID, decision.Replicas)
		if err != nil {
			return result, err
		}
		result.Status = status
		if status.Available < status.Desired {
			result.Pending = true
			return result, nil
		}
		updated, err := routeWithReadyInstances(route, status)
		if err != nil {
			return result, err
		}
		if err := e.routing.Reconcile(ctx, updated); err != nil {
			return result, fmt.Errorf("publish scaled route: %w", err)
		}
		result.Route = updated
		return result, nil
	case ActionRemoveReplica:
		status, err := e.orchestrator.Status(ctx, workloadID)
		if err != nil {
			return result, err
		}
		backendID := retiringBackend(route, status)
		if backendID == "" {
			return result, fmt.Errorf("No routable replica is available to drain")
		}
		if err := e.routing.Drain(ctx, route.ID, backendID); err != nil {
			return result, fmt.Errorf("drain replica: %w", err)
		}
		status, err = e.orchestrator.Scale(ctx, workloadID, decision.Replicas)
		result.Status = status
		if err == nil && e.routing.ID() == routing.ProviderTraefik {
			result.Route = route
			return result, nil
		}
		if err == nil {
			updated, routeErr := routeWithReadyInstances(route, status)
			if routeErr != nil {
				return result, routeErr
			}
			if routeErr = e.routing.Reconcile(ctx, updated); routeErr != nil {
				return result, fmt.Errorf("publish scaled route: %w", routeErr)
			}
			result.Route = updated
		}
		return result, err
	default:
		return result, fmt.Errorf("Unknown autoscaling action %q", decision.Action)
	}
}

func routeWithReadyInstances(route routing.Route, status orchestrator.Status) (routing.Route, error) {
	backends := make([]routing.Backend, 0, len(status.Instances))
	for _, instance := range status.Instances {
		if !instance.Ready || !instance.Healthy {
			continue
		}
		if instance.Address == "" {
			return route, fmt.Errorf("Ready instance %q has no routable address", instance.ID)
		}
		backends = append(backends, routing.Backend{ID: instance.ID, Address: instance.Address, Healthy: true, Weight: 1})
	}
	if len(backends) != status.Desired {
		return route, fmt.Errorf("Only %d of %d replicas are routable", len(backends), status.Desired)
	}
	route.Backends = backends
	return route, nil
}

func retiringBackend(route routing.Route, status orchestrator.Status) string {
	ready := make(map[string]bool, len(status.Instances))
	for _, instance := range status.Instances {
		ready[instance.ID] = instance.Ready
	}
	for index := len(route.Backends) - 1; index >= 0; index-- {
		if ready[route.Backends[index].ID] {
			return route.Backends[index].ID
		}
	}
	return ""
}
