package cluster

type Capability string

const (
	CapabilityFleetRead       Capability = "fleet.read"
	CapabilityDeploymentsRead Capability = "deployments.read"
	CapabilityDeploymentsRun  Capability = "deployments.run"
	CapabilityCapacityRead    Capability = "capacity.read"
	CapabilityCapacityOffer   Capability = "capacity.offer"
	CapabilityEventsPublish   Capability = "events.publish"
	CapabilityRoutingManage   Capability = "routing.manage"
)

type Grant struct {
	Capability  Capability `json:"capability"`
	Deployments []string   `json:"deployments,omitempty"`
	MaxCPU      float64    `json:"max_cpu,omitempty"`
	MaxMemory   uint64     `json:"max_memory,omitempty"`
	MaxReplicas int        `json:"max_replicas,omitempty"`
}
