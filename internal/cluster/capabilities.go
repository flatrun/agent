package cluster

type Capability string

const (
	CapabilityFleetRead         Capability = "fleet.read"
	CapabilityDeploymentsRead   Capability = "deployments.read"
	CapabilityDeploymentsRun    Capability = "deployments.run"
	CapabilityDeploymentsManage Capability = "deployments.manage"
	CapabilityCapacityRead      Capability = "capacity.read"
	CapabilityCapacityOffer     Capability = "capacity.offer"
	CapabilityEventsPublish     Capability = "events.publish"
	CapabilityRoutingManage     Capability = "routing.manage"
)

type Grant struct {
	Capability  Capability `json:"capability"`
	Deployments []string   `json:"deployments,omitempty"`
	MaxCPU      float64    `json:"max_cpu,omitempty"`
	MaxMemory   uint64     `json:"max_memory,omitempty"`
	MaxReplicas int        `json:"max_replicas,omitempty"`
}

type PeerPolicy struct {
	Peer   string  `json:"peer"`
	Grants []Grant `json:"grants"`
}

func DefaultPeerGrants() []Grant {
	return []Grant{
		{Capability: CapabilityFleetRead},
		{Capability: CapabilityDeploymentsRead},
		{Capability: CapabilityDeploymentsRun},
		{Capability: CapabilityCapacityRead},
	}
}

func ValidCapability(value Capability) bool {
	switch value {
	case CapabilityFleetRead, CapabilityDeploymentsRead, CapabilityDeploymentsRun, CapabilityDeploymentsManage,
		CapabilityCapacityRead, CapabilityCapacityOffer, CapabilityEventsPublish, CapabilityRoutingManage:
		return true
	default:
		return false
	}
}
