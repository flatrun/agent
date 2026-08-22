package autoscale

import (
	"context"
	"fmt"
	"time"

	"github.com/flatrun/agent/internal/orchestrator"
	"github.com/flatrun/agent/internal/routing"
)

type ServiceStopper interface {
	StopService(string, string) (string, error)
}

type ServiceStopperFunc func(string, string) (string, error)

func (f ServiceStopperFunc) StopService(deployment, service string) (string, error) {
	return f(deployment, service)
}

type Activator struct {
	orchestrator orchestrator.Provider
	routing      routing.Provider
	stopper      ServiceStopper
	pollInterval time.Duration
	readyTimeout time.Duration
}

type Activation struct {
	Workload orchestrator.Status `json:"workload"`
	Route    routing.Route       `json:"route"`
}

func NewActivator(orchestratorProvider orchestrator.Provider, routingProvider routing.Provider, stopper ServiceStopper) *Activator {
	return &Activator{orchestrator: orchestratorProvider, routing: routingProvider, stopper: stopper, pollInterval: 2 * time.Second, readyTimeout: 2 * time.Minute}
}

func (a *Activator) Activate(ctx context.Context, deployment, service string, workload orchestrator.Workload, route routing.Route) (Activation, error) {
	return a.ActivateDurably(ctx, deployment, service, workload, route, nil)
}

func (a *Activator) ActivateDurably(ctx context.Context, deployment, service string, workload orchestrator.Workload, route routing.Route, persist func(Activation) error) (Activation, error) {
	status, err := a.orchestrator.Apply(ctx, workload)
	if err != nil {
		return Activation{}, fmt.Errorf("create managed workload: %w", err)
	}
	rollback := func() {
		_ = a.routing.Remove(context.Background(), route.ID)
		_ = a.orchestrator.Remove(context.Background(), workload.ID)
	}
	status, err = a.waitReady(ctx, workload.ID, status)
	if err != nil {
		rollback()
		return Activation{}, err
	}
	route, err = routeWithReadyInstances(route, status)
	if err != nil {
		rollback()
		return Activation{}, err
	}
	if err := a.routing.Reconcile(ctx, route); err != nil {
		rollback()
		return Activation{}, fmt.Errorf("publish managed route: %w", err)
	}
	activation := Activation{Workload: status, Route: route}
	if persist != nil {
		if err := persist(activation); err != nil {
			rollback()
			return Activation{}, fmt.Errorf("save managed workload state: %w", err)
		}
	}
	if _, err := a.stopper.StopService(deployment, service); err != nil {
		rollback()
		return Activation{}, fmt.Errorf("stop Compose service after cutover: %w", err)
	}
	return activation, nil
}

func (a *Activator) waitReady(ctx context.Context, workloadID string, status orchestrator.Status) (orchestrator.Status, error) {
	if status.Desired > 0 && status.Available >= status.Desired {
		return status, nil
	}
	timer := time.NewTimer(a.readyTimeout)
	defer timer.Stop()
	ticker := time.NewTicker(a.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-timer.C:
			return status, fmt.Errorf("managed workload did not become ready within %s", a.readyTimeout)
		case <-ticker.C:
			var err error
			status, err = a.orchestrator.Status(ctx, workloadID)
			if err != nil {
				return status, fmt.Errorf("check managed workload readiness: %w", err)
			}
			if status.Desired > 0 && status.Available >= status.Desired {
				return status, nil
			}
		}
	}
}
