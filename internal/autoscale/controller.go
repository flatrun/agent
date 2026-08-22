package autoscale

import (
	"fmt"
	"math"
	"time"

	"github.com/flatrun/agent/internal/capacity"
	"github.com/flatrun/agent/internal/orchestrator"
)

type Action string

const (
	ActionNone           Action = "none"
	ActionIncreaseCPU    Action = "increase_cpu"
	ActionIncreaseMemory Action = "increase_memory"
	ActionAddReplica     Action = "add_replica"
	ActionRemoveReplica  Action = "remove_replica"
	ActionNotify         Action = "notify"
)

type Policy struct {
	Enabled            bool          `json:"enabled"`
	MinReplicas        int           `json:"min_replicas"`
	MaxReplicas        int           `json:"max_replicas"`
	ScaleUpPercent     float64       `json:"scale_up_percent"`
	ScaleDownPercent   float64       `json:"scale_down_percent"`
	ScaleUpWindows     int           `json:"scale_up_windows"`
	ScaleDownWindows   int           `json:"scale_down_windows"`
	Cooldown           time.Duration `json:"cooldown"`
	AllowFleetCapacity bool          `json:"allow_fleet_capacity"`
}

type State struct {
	HighWindows int       `json:"high_windows"`
	LowWindows  int       `json:"low_windows"`
	LastAction  time.Time `json:"last_action,omitempty"`
}

type Input struct {
	Now               time.Time
	Replicas          int
	CPUPercent        float64
	MemoryPercent     float64
	Diagnosis         capacity.Diagnosis
	FleetOffer        capacity.Offer
	RequiresFleet     bool
	CurrentResources  orchestrator.Resources
	SuggestedResource orchestrator.Resources
}

type Decision struct {
	Action    Action                 `json:"action"`
	Replicas  int                    `json:"replicas,omitempty"`
	Resources orchestrator.Resources `json:"resources,omitempty"`
	Reason    string                 `json:"reason"`
}

func DefaultPolicy() Policy {
	return Policy{
		Enabled: true, MinReplicas: 1, MaxReplicas: 3,
		ScaleUpPercent: 80, ScaleDownPercent: 30,
		ScaleUpWindows: 3, ScaleDownWindows: 10,
		Cooldown: 5 * time.Minute,
	}
}

func Reconcile(policy Policy, state State, input Input) (State, Decision) {
	if err := ValidatePolicy(policy); err != nil {
		return state, Decision{Action: ActionNotify, Reason: err.Error()}
	}
	if !policy.Enabled {
		return State{}, Decision{Action: ActionNone, Reason: "Autoscaling is disabled"}
	}
	if input.Now.IsZero() {
		input.Now = time.Now()
	}
	if input.Replicas < policy.MinReplicas {
		return acted(state, input.Now), Decision{Action: ActionAddReplica, Replicas: policy.MinReplicas, Reason: "Replica count is below policy minimum"}
	}
	if !state.LastAction.IsZero() && input.Now.Sub(state.LastAction) < policy.Cooldown {
		return state, Decision{Action: ActionNone, Reason: "Autoscaling is in cooldown"}
	}

	switch input.Diagnosis.Action {
	case capacity.ActionIncreaseCPU:
		return acted(state, input.Now), Decision{Action: ActionIncreaseCPU, Resources: input.SuggestedResource, Reason: input.Diagnosis.Reason}
	case capacity.ActionIncreaseMemory:
		return acted(state, input.Now), Decision{Action: ActionIncreaseMemory, Resources: input.SuggestedResource, Reason: input.Diagnosis.Reason}
	}

	utilization := math.Max(input.CPUPercent, input.MemoryPercent)
	if utilization >= policy.ScaleUpPercent {
		state.HighWindows++
		state.LowWindows = 0
	} else if utilization <= policy.ScaleDownPercent {
		state.LowWindows++
		state.HighWindows = 0
	} else {
		state.HighWindows = 0
		state.LowWindows = 0
	}

	if state.HighWindows >= policy.ScaleUpWindows {
		if input.Replicas >= policy.MaxReplicas {
			return state, Decision{Action: ActionNotify, Reason: "Workload has reached its replica limit"}
		}
		if input.RequiresFleet && (!policy.AllowFleetCapacity || !input.FleetOffer.Enabled) {
			return state, Decision{Action: ActionNotify, Reason: "No permitted fleet capacity is available"}
		}
		return acted(state, input.Now), Decision{Action: ActionAddReplica, Replicas: input.Replicas + 1, Reason: "Resource pressure remained above the scale-up threshold"}
	}
	if state.LowWindows >= policy.ScaleDownWindows && input.Replicas > policy.MinReplicas {
		return acted(state, input.Now), Decision{Action: ActionRemoveReplica, Replicas: input.Replicas - 1, Reason: "Resource use remained below the scale-down threshold"}
	}
	return state, Decision{Action: ActionNone, Reason: "No scaling threshold has been sustained"}
}

func ValidatePolicy(policy Policy) error {
	if policy.MinReplicas < 1 || policy.MaxReplicas < policy.MinReplicas {
		return fmt.Errorf("Replica limits are invalid")
	}
	if policy.ScaleUpPercent <= policy.ScaleDownPercent || policy.ScaleUpPercent > 100 || policy.ScaleDownPercent < 0 {
		return fmt.Errorf("Scaling thresholds are invalid")
	}
	if policy.ScaleUpWindows < 1 || policy.ScaleDownWindows < 1 || policy.Cooldown < 0 {
		return fmt.Errorf("Scaling timing is invalid")
	}
	return nil
}

func acted(state State, now time.Time) State {
	state.HighWindows = 0
	state.LowWindows = 0
	state.LastAction = now
	return state
}
