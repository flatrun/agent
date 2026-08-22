package capacity

import "math"

type Pressure string

const (
	PressureNone       Pressure = "none"
	PressureAllocation Pressure = "allocation"
	PressureHost       Pressure = "host"
)

type Action string

const (
	ActionNone           Action = "none"
	ActionIncreaseMemory Action = "increase_memory"
	ActionIncreaseCPU    Action = "increase_cpu"
	ActionAddReplica     Action = "add_replica"
	ActionNotify         Action = "notify"
)

type Host struct {
	CPUCores        float64 `json:"cpu_cores"`
	CPUUsagePercent float64 `json:"cpu_usage_percent"`
	MemoryTotal     uint64  `json:"memory_total"`
	MemoryAvailable uint64  `json:"memory_available"`
}

type Container struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	CPUPercent  float64 `json:"cpu_percent"`
	CPULimit    float64 `json:"cpu_limit"`
	MemoryUsage uint64  `json:"memory_usage"`
	MemoryLimit uint64  `json:"memory_limit"`
}

type Policy struct {
	AllocationThresholdPercent float64 `json:"allocation_threshold_percent"`
	HostThresholdPercent       float64 `json:"host_threshold_percent"`
	HostMemoryReserve          uint64  `json:"host_memory_reserve"`
	HostCPUReserve             float64 `json:"host_cpu_reserve"`
	MemoryStepPercent          float64 `json:"memory_step_percent"`
	CPUStepPercent             float64 `json:"cpu_step_percent"`
	MaxMemory                  uint64  `json:"max_memory"`
	MaxCPU                     float64 `json:"max_cpu"`
	AllowVertical              bool    `json:"allow_vertical"`
	AllowHorizontal            bool    `json:"allow_horizontal"`
}

type Diagnosis struct {
	Pressure         Pressure `json:"pressure"`
	Resource         string   `json:"resource,omitempty"`
	Action           Action   `json:"action"`
	CurrentLimit     float64  `json:"current_limit,omitempty"`
	RecommendedLimit float64  `json:"recommended_limit,omitempty"`
	Reason           string   `json:"reason"`
}

func DefaultPolicy() Policy {
	return Policy{
		AllocationThresholdPercent: 90,
		HostThresholdPercent:       85,
		HostMemoryReserve:          512 * 1024 * 1024,
		HostCPUReserve:             0.25,
		MemoryStepPercent:          50,
		CPUStepPercent:             50,
		AllowVertical:              true,
		AllowHorizontal:            true,
	}
}

func Diagnose(host Host, container Container, policy Policy) Diagnosis {
	if policy.AllocationThresholdPercent <= 0 {
		policy = DefaultPolicy()
	}

	if container.MemoryLimit > 0 {
		utilization := float64(container.MemoryUsage) / float64(container.MemoryLimit) * 100
		if utilization >= policy.AllocationThresholdPercent {
			next := grow(float64(container.MemoryLimit), policy.MemoryStepPercent, float64(policy.MaxMemory))
			required := uint64(math.Max(0, next-float64(container.MemoryLimit)))
			if policy.AllowVertical && next > float64(container.MemoryLimit) && host.MemoryAvailable >= policy.HostMemoryReserve+required {
				return Diagnosis{Pressure: PressureAllocation, Resource: "memory", Action: ActionIncreaseMemory, CurrentLimit: float64(container.MemoryLimit), RecommendedLimit: next, Reason: "Container memory is constrained while the host has reserved headroom"}
			}
			return exhaustedAction("memory", policy)
		}
	}

	if container.CPULimit > 0 {
		utilization := container.CPUPercent / (container.CPULimit * 100) * 100
		if utilization >= policy.AllocationThresholdPercent {
			next := grow(container.CPULimit, policy.CPUStepPercent, policy.MaxCPU)
			available := host.CPUCores * math.Max(0, 100-host.CPUUsagePercent) / 100
			if policy.AllowVertical && next > container.CPULimit && available >= policy.HostCPUReserve+(next-container.CPULimit) {
				return Diagnosis{Pressure: PressureAllocation, Resource: "cpu", Action: ActionIncreaseCPU, CurrentLimit: container.CPULimit, RecommendedLimit: next, Reason: "Container CPU is constrained while the host has reserved headroom"}
			}
			return exhaustedAction("cpu", policy)
		}
	}

	memoryPressure := host.MemoryTotal > 0 && float64(host.MemoryTotal-host.MemoryAvailable)/float64(host.MemoryTotal)*100 >= policy.HostThresholdPercent
	if memoryPressure || host.CPUUsagePercent >= policy.HostThresholdPercent {
		resource := "cpu"
		if memoryPressure {
			resource = "memory"
		}
		return Diagnosis{Pressure: PressureHost, Resource: resource, Action: horizontalOrNotify(policy), Reason: "Host capacity is below its configured safety reserve"}
	}

	return Diagnosis{Pressure: PressureNone, Action: ActionNone, Reason: "Container allocation and host capacity are within policy"}
}

func grow(current, percent, maximum float64) float64 {
	next := current * (1 + percent/100)
	if maximum > 0 && next > maximum {
		return maximum
	}
	return next
}

func exhaustedAction(resource string, policy Policy) Diagnosis {
	return Diagnosis{Pressure: PressureHost, Resource: resource, Action: horizontalOrNotify(policy), Reason: "Container allocation is constrained and the host cannot safely increase it"}
}

func horizontalOrNotify(policy Policy) Action {
	if policy.AllowHorizontal {
		return ActionAddReplica
	}
	return ActionNotify
}
