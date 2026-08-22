package autoscale

import (
	"testing"
	"time"

	"github.com/flatrun/agent/internal/capacity"
	"github.com/flatrun/agent/internal/orchestrator"
)

func TestReconcileIncreasesLocalAllocationBeforeReplicas(t *testing.T) {
	policy := DefaultPolicy()
	now := time.Now()
	state, decision := Reconcile(policy, State{HighWindows: policy.ScaleUpWindows - 1}, Input{
		Now: now, Replicas: 1, CPUPercent: 95,
		Diagnosis:         capacity.Diagnosis{Action: capacity.ActionIncreaseCPU, Reason: "Host has headroom"},
		SuggestedResource: orchestrator.Resources{CPULimit: 2},
	})

	if decision.Action != ActionIncreaseCPU || decision.Resources.CPULimit != 2 {
		t.Fatalf("decision = %#v", decision)
	}
	if !state.LastAction.Equal(now) || state.HighWindows != 0 {
		t.Fatalf("state = %#v", state)
	}
}

func TestReconcileRequiresSustainedPressureToAddReplica(t *testing.T) {
	policy := DefaultPolicy()
	now := time.Now()
	state := State{}
	for window := 1; window <= policy.ScaleUpWindows; window++ {
		var decision Decision
		state, decision = Reconcile(policy, state, Input{Now: now, Replicas: 1, CPUPercent: 90})
		if window < policy.ScaleUpWindows && decision.Action != ActionNone {
			t.Fatalf("window %d decision = %#v", window, decision)
		}
		if window == policy.ScaleUpWindows && (decision.Action != ActionAddReplica || decision.Replicas != 2) {
			t.Fatalf("window %d decision = %#v", window, decision)
		}
	}
}

func TestReconcileRequiresPermittedFleetCapacity(t *testing.T) {
	policy := DefaultPolicy()
	policy.AllowFleetCapacity = true
	state := State{HighWindows: policy.ScaleUpWindows - 1}
	_, decision := Reconcile(policy, state, Input{
		Now: time.Now(), Replicas: 1, CPUPercent: 95, RequiresFleet: true,
		FleetOffer: capacity.Offer{Enabled: false},
	})
	if decision.Action != ActionNotify || decision.Reason != "No permitted fleet capacity is available" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestReconcileRemovesReplicaAfterSustainedRecovery(t *testing.T) {
	policy := DefaultPolicy()
	state := State{LowWindows: policy.ScaleDownWindows - 1}
	_, decision := Reconcile(policy, state, Input{Now: time.Now(), Replicas: 3, CPUPercent: 10, MemoryPercent: 20})
	if decision.Action != ActionRemoveReplica || decision.Replicas != 2 {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestReconcileHonorsCooldown(t *testing.T) {
	policy := DefaultPolicy()
	now := time.Now()
	state := State{HighWindows: policy.ScaleUpWindows - 1, LastAction: now.Add(-time.Minute)}
	unchanged, decision := Reconcile(policy, state, Input{Now: now, Replicas: 1, CPUPercent: 99})
	if decision.Action != ActionNone || unchanged.HighWindows != state.HighWindows {
		t.Fatalf("state = %#v, decision = %#v", unchanged, decision)
	}
}

func TestReconcileDisabledPreservesManagedWorkloadState(t *testing.T) {
	policy := DefaultPolicy()
	policy.Enabled = false
	state, decision := Reconcile(policy, State{
		HighWindows: 2, LowWindows: 3, Active: true,
		Provider: orchestrator.ProviderSwarm, Service: "web", Replicas: 2,
	}, Input{})
	if decision.Action != ActionNone || state.HighWindows != 0 || state.LowWindows != 0 {
		t.Fatalf("state = %#v, decision = %#v", state, decision)
	}
	if !state.Active || state.Provider != orchestrator.ProviderSwarm || state.Service != "web" || state.Replicas != 2 {
		t.Fatalf("runtime state was cleared: %#v", state)
	}
}
