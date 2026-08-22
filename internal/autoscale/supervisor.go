package autoscale

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/flatrun/agent/internal/events"
)

type ActiveStore interface {
	PolicyStore
	ActiveStates() (map[string]State, error)
}

type RuntimeSession struct {
	Input    Input
	Executor ActionExecutor
	Close    func() error
}

type RuntimeFactory interface {
	Build(context.Context, string, State) (RuntimeSession, error)
}

type Supervisor struct {
	store     ActiveStore
	factory   RuntimeFactory
	publisher EventPublisher
	node      string
	interval  time.Duration
	mu        sync.Mutex
}

func NewSupervisor(store ActiveStore, factory RuntimeFactory, publisher EventPublisher, node string, interval time.Duration) *Supervisor {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Supervisor{store: store, factory: factory, publisher: publisher, node: node, interval: interval}
}

func (s *Supervisor) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.Tick(ctx)
		}
	}
}

func (s *Supervisor) Tick(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	states, err := s.store.ActiveStates()
	if err != nil {
		return fmt.Errorf("load active autoscaling workloads: %w", err)
	}
	var firstErr error
	for deployment, state := range states {
		session, err := s.factory.Build(ctx, deployment, state)
		if err == nil {
			_, err = NewRunner(s.store, session.Executor, s.publisher, s.node).Reconcile(ctx, deployment, session.Input, state.Route)
			if session.Close != nil {
				if closeErr := session.Close(); err == nil {
					err = closeErr
				}
			}
		}
		if err != nil {
			s.publishFailure(deployment, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (s *Supervisor) publishFailure(deployment string, err error) {
	if s.publisher == nil {
		return
	}
	_, _ = s.publisher.Publish(events.Event{
		Source: "capacity", Type: "autoscale.observation_failed", Severity: events.SeverityCritical,
		Title: "Autoscaling observation failed", Message: err.Error(),
		Scope:          events.Scope{Node: s.node, Deployment: deployment},
		CorrelationKey: "autoscale-observation:" + s.node + ":" + deployment, OccurredAt: time.Now(),
	})
}
