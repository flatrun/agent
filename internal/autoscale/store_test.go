package autoscale

import (
	"testing"
	"time"

	"github.com/flatrun/agent/internal/orchestrator"
	"github.com/flatrun/agent/internal/routing"
)

func TestStorePersistsPolicyAndState(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	policy := DefaultPolicy()
	policy.MaxReplicas = 7
	policy.AllowFleetCapacity = true
	state := State{
		HighWindows: 2, LastAction: time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC), Active: true,
		Provider: orchestrator.ProviderSwarm, Service: "web", Replicas: 2,
		Route: routing.Route{ID: "shop", Service: "web", Domain: "shop.example.com", Protocol: "http"},
	}
	if err := store.SetPolicy("shop", policy); err != nil {
		t.Fatal(err)
	}
	if err := store.SetState("shop", state); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	savedPolicy, err := store.Policy("shop")
	if err != nil {
		t.Fatal(err)
	}
	savedState, err := store.State("shop")
	if err != nil {
		t.Fatal(err)
	}
	if savedPolicy.MaxReplicas != 7 || !savedPolicy.AllowFleetCapacity {
		t.Fatalf("unexpected policy: %+v", savedPolicy)
	}
	if savedState.HighWindows != 2 || !savedState.LastAction.Equal(state.LastAction) {
		t.Fatalf("unexpected state: %+v", savedState)
	}
	active, err := store.ActiveStates()
	if err != nil {
		t.Fatal(err)
	}
	if active["shop"].Route.Domain != "shop.example.com" || active["shop"].Provider != orchestrator.ProviderSwarm {
		t.Fatalf("unexpected active states: %+v", active)
	}
}

func TestStoreRejectsInvalidPolicy(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	policy := DefaultPolicy()
	policy.MaxReplicas = 0
	if err := store.SetPolicy("shop", policy); err == nil {
		t.Fatal("expected invalid policy error")
	}
}
