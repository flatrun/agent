package orchestrator

import "context"

type ProviderID string

const (
	ProviderStandalone ProviderID = "standalone"
	ProviderSwarm      ProviderID = "swarm"
	ProviderK3s        ProviderID = "k3s"
)

type Resources struct {
	CPURequest    float64 `json:"cpu_request,omitempty"`
	CPULimit      float64 `json:"cpu_limit,omitempty"`
	MemoryRequest uint64  `json:"memory_request,omitempty"`
	MemoryLimit   uint64  `json:"memory_limit,omitempty"`
}

type Health struct {
	Path             string `json:"path,omitempty"`
	IntervalSeconds  int    `json:"interval_seconds,omitempty"`
	TimeoutSeconds   int    `json:"timeout_seconds,omitempty"`
	HealthyThreshold int    `json:"healthy_threshold,omitempty"`
}

type Workload struct {
	ID        string            `json:"id"`
	Image     string            `json:"image"`
	Replicas  int               `json:"replicas"`
	Resources Resources         `json:"resources"`
	Health    Health            `json:"health"`
	Labels    map[string]string `json:"labels,omitempty"`
	Stateful  bool              `json:"stateful"`
}

type Instance struct {
	ID      string `json:"id"`
	Node    string `json:"node"`
	Address string `json:"address"`
	Healthy bool   `json:"healthy"`
	Ready   bool   `json:"ready"`
}

type Status struct {
	Workload  string     `json:"workload"`
	Desired   int        `json:"desired"`
	Available int        `json:"available"`
	Instances []Instance `json:"instances"`
}

type Provider interface {
	ID() ProviderID
	Validate(context.Context, Workload) error
	Apply(context.Context, Workload) (Status, error)
	Resize(context.Context, string, Resources) (Status, error)
	Scale(context.Context, string, int) (Status, error)
	Status(context.Context, string) (Status, error)
	Remove(context.Context, string) error
}
