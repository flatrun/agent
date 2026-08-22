package autoscale

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/orchestrator"
)

type supervisorStore struct {
	runnerStore
	states map[string]State
}

func (s *supervisorStore) ActiveStates() (map[string]State, error) { return s.states, nil }

type runtimeFactoryStub struct {
	err      error
	builtFor string
	executor *runnerExecutor
}

func (f *runtimeFactoryStub) Build(_ context.Context, deployment string, state State) (RuntimeSession, error) {
	f.builtFor = deployment
	return RuntimeSession{Input: Input{Now: time.Now(), Replicas: state.Replicas}, Executor: f.executor}, f.err
}

func TestSupervisorReconcilesActiveWorkloads(t *testing.T) {
	policy := DefaultPolicy()
	store := &supervisorStore{
		runnerStore: runnerStore{policy: policy, state: State{Active: true, Replicas: 1}},
		states:      map[string]State{"shop": {Active: true, Replicas: 1}},
	}
	factory := &runtimeFactoryStub{executor: &runnerExecutor{}}
	if err := NewSupervisor(store, factory, nil, "prod-1", time.Second).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if factory.builtFor != "shop" {
		t.Fatalf("built for %q", factory.builtFor)
	}
}

func TestSupervisorPublishesObservationFailure(t *testing.T) {
	store := &supervisorStore{
		runnerStore: runnerStore{policy: DefaultPolicy()},
		states:      map[string]State{"shop": {Active: true, Provider: orchestrator.ProviderSwarm}},
	}
	publisher := &runnerPublisher{}
	err := NewSupervisor(store, &runtimeFactoryStub{err: errors.New("stats unavailable")}, publisher, "prod-1", time.Second).Tick(context.Background())
	if err == nil || len(publisher.events) != 1 || publisher.events[0].CorrelationKey != "autoscale-observation:prod-1:shop" {
		t.Fatalf("error = %v, events = %#v", err, publisher.events)
	}
}
