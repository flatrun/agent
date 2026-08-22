package orchestrator

import (
	"context"
	"net/netip"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

type fakeSwarmClient struct {
	service swarm.Service
	tasks   []swarm.Task
	created *swarm.ServiceSpec
	updated *swarm.ServiceSpec
}

func (f *fakeSwarmClient) SwarmInspect(_ context.Context, _ client.SwarmInspectOptions) (client.SwarmInspectResult, error) {
	return client.SwarmInspectResult{Swarm: swarm.Swarm{ClusterInfo: swarm.ClusterInfo{ID: "swarm-1"}}}, nil
}

func (f *fakeSwarmClient) ServiceCreate(_ context.Context, options client.ServiceCreateOptions) (client.ServiceCreateResult, error) {
	f.created = &options.Spec
	f.service = swarm.Service{ID: "service-1", Spec: options.Spec}
	return client.ServiceCreateResult{ID: f.service.ID}, nil
}

func (f *fakeSwarmClient) ServiceInspect(_ context.Context, _ string, _ client.ServiceInspectOptions) (client.ServiceInspectResult, error) {
	if f.service.ID == "" {
		return client.ServiceInspectResult{}, errdefs.ErrNotFound
	}
	return client.ServiceInspectResult{Service: f.service}, nil
}

func (f *fakeSwarmClient) ServiceUpdate(_ context.Context, _ string, options client.ServiceUpdateOptions) (client.ServiceUpdateResult, error) {
	f.updated = &options.Spec
	f.service.Spec = options.Spec
	return client.ServiceUpdateResult{}, nil
}

func (f *fakeSwarmClient) ServiceRemove(_ context.Context, _ string, _ client.ServiceRemoveOptions) (client.ServiceRemoveResult, error) {
	f.service = swarm.Service{}
	return client.ServiceRemoveResult{}, nil
}

func (f *fakeSwarmClient) TaskList(_ context.Context, _ client.TaskListOptions) (client.TaskListResult, error) {
	return client.TaskListResult{Items: f.tasks}, nil
}

func TestSwarmProviderCreatesReplicatedService(t *testing.T) {
	client := &fakeSwarmClient{tasks: []swarm.Task{{
		ID: "task-1", NodeID: "node-1", DesiredState: swarm.TaskStateRunning,
		Status:              swarm.TaskStatus{State: swarm.TaskStateRunning},
		NetworksAttachments: []swarm.NetworkAttachment{{Addresses: []netip.Prefix{netip.MustParsePrefix("10.0.0.12/24")}}},
	}}}
	provider := NewSwarmProvider(client)

	status, err := provider.Apply(context.Background(), Workload{
		ID: "shop", Image: "example/shop:1", Port: 8080, Replicas: 1,
		Resources:   Resources{CPURequest: 0.5, CPULimit: 1, MemoryRequest: 256 << 20, MemoryLimit: 512 << 20},
		Environment: map[string]string{"APP_ENV": "production"}, Entrypoint: []string{"/app/entrypoint"}, Command: []string{"serve"}, WorkingDir: "/app",
		Networks: []string{"proxy"},
	})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if client.created == nil || client.created.Annotations.Name != "shop" {
		t.Fatalf("created spec = %#v", client.created)
	}
	if client.created.TaskTemplate.Resources.Limits.NanoCPUs != 1_000_000_000 {
		t.Fatalf("CPU limit = %d", client.created.TaskTemplate.Resources.Limits.NanoCPUs)
	}
	container := client.created.TaskTemplate.ContainerSpec
	if len(container.Env) != 1 || container.Env[0] != "APP_ENV=production" || container.Command[0] != "/app/entrypoint" || container.Args[0] != "serve" || container.Dir != "/app" {
		t.Fatalf("container spec = %#v", container)
	}
	if len(client.created.TaskTemplate.Networks) != 1 || client.created.TaskTemplate.Networks[0].Target != "proxy" {
		t.Fatalf("networks = %#v", client.created.TaskTemplate.Networks)
	}
	if status.Desired != 1 || status.Available != 1 || len(status.Instances) != 1 {
		t.Fatalf("status = %#v", status)
	}
	if status.Instances[0].Address != "10.0.0.12:8080" {
		t.Fatalf("instance address = %q", status.Instances[0].Address)
	}
}

func TestSwarmProviderScalesExistingService(t *testing.T) {
	replicas := uint64(1)
	client := &fakeSwarmClient{service: swarm.Service{
		ID:   "service-1",
		Spec: swarm.ServiceSpec{Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &replicas}}},
	}}
	provider := NewSwarmProvider(client)

	status, err := provider.Scale(context.Background(), "shop", 3)
	if err != nil {
		t.Fatalf("Scale failed: %v", err)
	}
	if client.updated == nil || *client.updated.Mode.Replicated.Replicas != 3 {
		t.Fatalf("updated spec = %#v", client.updated)
	}
	if status.Desired != 3 {
		t.Fatalf("status = %#v", status)
	}
}

func TestSwarmProviderRejectsUnsafeStatefulReplication(t *testing.T) {
	provider := NewSwarmProvider(&fakeSwarmClient{})
	err := provider.Validate(context.Background(), Workload{ID: "database", Image: "postgres:17", Replicas: 2, Stateful: true})
	if err == nil {
		t.Fatal("stateful replication should require a storage policy")
	}
}
