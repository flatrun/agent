package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

type swarmClient interface {
	SwarmInspect(context.Context, client.SwarmInspectOptions) (client.SwarmInspectResult, error)
	ServiceCreate(context.Context, client.ServiceCreateOptions) (client.ServiceCreateResult, error)
	ServiceInspect(context.Context, string, client.ServiceInspectOptions) (client.ServiceInspectResult, error)
	ServiceUpdate(context.Context, string, client.ServiceUpdateOptions) (client.ServiceUpdateResult, error)
	ServiceRemove(context.Context, string, client.ServiceRemoveOptions) (client.ServiceRemoveResult, error)
	TaskList(context.Context, client.TaskListOptions) (client.TaskListResult, error)
}

type SwarmProvider struct {
	client swarmClient
}

func NewSwarmProvider(client swarmClient) *SwarmProvider {
	return &SwarmProvider{client: client}
}

func NewSwarmProviderFromEnv() (*SwarmProvider, error) {
	cli, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return NewSwarmProvider(cli), nil
}

func (p *SwarmProvider) Ready(ctx context.Context) error {
	if _, err := p.client.SwarmInspect(ctx, client.SwarmInspectOptions{}); err != nil {
		return fmt.Errorf("Docker Swarm is not available: %w", err)
	}
	return nil
}

func (p *SwarmProvider) ID() ProviderID {
	return ProviderSwarm
}

func (p *SwarmProvider) Validate(_ context.Context, workload Workload) error {
	if strings.TrimSpace(workload.ID) == "" {
		return fmt.Errorf("Workload ID is required")
	}
	if strings.TrimSpace(workload.Image) == "" {
		return fmt.Errorf("Workload image is required")
	}
	if workload.Replicas < 1 {
		return fmt.Errorf("Replicas must be at least one")
	}
	if workload.Stateful && workload.Replicas > 1 {
		return fmt.Errorf("Stateful workloads cannot use multiple replicas without a storage policy")
	}
	return nil
}

func (p *SwarmProvider) Apply(ctx context.Context, workload Workload) (Status, error) {
	if err := p.Validate(ctx, workload); err != nil {
		return Status{}, err
	}
	inspected, err := p.client.ServiceInspect(ctx, workload.ID, client.ServiceInspectOptions{})
	if err != nil {
		if !errdefs.IsNotFound(err) {
			return Status{}, fmt.Errorf("inspect Swarm service: %w", err)
		}
		if _, err := p.client.ServiceCreate(ctx, client.ServiceCreateOptions{Spec: swarmSpec(workload)}); err != nil {
			return Status{}, fmt.Errorf("create Swarm service: %w", err)
		}
		return p.Status(ctx, workload.ID)
	}
	if _, err := p.client.ServiceUpdate(ctx, inspected.Service.ID, client.ServiceUpdateOptions{
		Version: inspected.Service.Version,
		Spec:    swarmSpec(workload),
	}); err != nil {
		return Status{}, fmt.Errorf("update Swarm service: %w", err)
	}
	return p.Status(ctx, workload.ID)
}

func (p *SwarmProvider) Resize(ctx context.Context, id string, resources Resources) (Status, error) {
	service, err := p.client.ServiceInspect(ctx, id, client.ServiceInspectOptions{})
	if err != nil {
		return Status{}, err
	}
	service.Service.Spec.TaskTemplate.Resources = swarmResources(resources)
	if _, err := p.client.ServiceUpdate(ctx, service.Service.ID, client.ServiceUpdateOptions{
		Version: service.Service.Version,
		Spec:    service.Service.Spec,
	}); err != nil {
		return Status{}, fmt.Errorf("resize Swarm service: %w", err)
	}
	return p.Status(ctx, id)
}

func (p *SwarmProvider) Scale(ctx context.Context, id string, replicas int) (Status, error) {
	if replicas < 0 {
		return Status{}, fmt.Errorf("Replicas cannot be negative")
	}
	service, err := p.client.ServiceInspect(ctx, id, client.ServiceInspectOptions{})
	if err != nil {
		return Status{}, err
	}
	count := uint64(replicas)
	service.Service.Spec.Mode = swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &count}}
	if _, err := p.client.ServiceUpdate(ctx, service.Service.ID, client.ServiceUpdateOptions{
		Version: service.Service.Version,
		Spec:    service.Service.Spec,
	}); err != nil {
		return Status{}, fmt.Errorf("scale Swarm service: %w", err)
	}
	return p.Status(ctx, id)
}

func (p *SwarmProvider) Status(ctx context.Context, id string) (Status, error) {
	service, err := p.client.ServiceInspect(ctx, id, client.ServiceInspectOptions{})
	if err != nil {
		return Status{}, err
	}
	tasks, err := p.client.TaskList(ctx, client.TaskListOptions{Filters: make(client.Filters).Add("service", service.Service.ID)})
	if err != nil {
		return Status{}, fmt.Errorf("list Swarm tasks: %w", err)
	}
	status := Status{Workload: id}
	if service.Service.Spec.Mode.Replicated != nil && service.Service.Spec.Mode.Replicated.Replicas != nil {
		status.Desired = int(*service.Service.Spec.Mode.Replicated.Replicas)
	}
	for _, task := range tasks.Items {
		if task.DesiredState == swarm.TaskStateShutdown || task.DesiredState == swarm.TaskStateRemove {
			continue
		}
		running := task.Status.State == swarm.TaskStateRunning
		if running {
			status.Available++
		}
		status.Instances = append(status.Instances, Instance{
			ID: task.ID, Node: task.NodeID, Healthy: running, Ready: running,
		})
	}
	return status, nil
}

func (p *SwarmProvider) Remove(ctx context.Context, id string) error {
	if _, err := p.client.ServiceRemove(ctx, id, client.ServiceRemoveOptions{}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("remove Swarm service: %w", err)
	}
	return nil
}

func swarmSpec(workload Workload) swarm.ServiceSpec {
	replicas := uint64(workload.Replicas)
	labels := make(map[string]string, len(workload.Labels)+1)
	for key, value := range workload.Labels {
		labels[key] = value
	}
	labels["flatrun.workload"] = workload.ID
	return swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: workload.ID, Labels: labels},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{Image: workload.Image, Labels: labels},
			Resources:     swarmResources(workload.Resources),
		},
		Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &replicas}},
	}
}

func swarmResources(resources Resources) *swarm.ResourceRequirements {
	return &swarm.ResourceRequirements{
		Limits: &swarm.Limit{
			NanoCPUs:    int64(resources.CPULimit * 1_000_000_000),
			MemoryBytes: int64(resources.MemoryLimit),
		},
		Reservations: &swarm.Resources{
			NanoCPUs:    int64(resources.CPURequest * 1_000_000_000),
			MemoryBytes: int64(resources.MemoryRequest),
		},
	}
}
