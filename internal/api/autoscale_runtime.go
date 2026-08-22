package api

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/flatrun/agent/internal/autoscale"
	"github.com/flatrun/agent/internal/capacity"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/orchestrator"
	"github.com/flatrun/agent/internal/routing"
	"github.com/flatrun/agent/internal/system"
	"github.com/flatrun/agent/pkg/config"
)

type autoscaleRuntimeFactory struct {
	server *Server
}

type autoscaleReplicaObservation struct {
	Stats  docker.ContainerStats
	Limits docker.ResourceLimits
}

func (f autoscaleRuntimeFactory) Build(ctx context.Context, deployment string, state autoscale.State) (autoscale.RuntimeSession, error) {
	if state.Provider == orchestrator.ProviderK3s {
		provider := orchestrator.NewK3sProvider(f.server.config.Cluster.K3s.Kubeconfig, f.server.config.Cluster.K3s.Namespace)
		status, err := provider.Status(ctx, deployment)
		if err != nil {
			return autoscale.RuntimeSession{}, err
		}
		usage, err := provider.Metrics(ctx, deployment)
		if err != nil {
			return autoscale.RuntimeSession{}, err
		}
		routeProvider := routing.NewK3sIngressProvider(f.server.config.Cluster.K3s.Kubeconfig, f.server.config.Cluster.K3s.Namespace, state.Route)
		return autoscale.RuntimeSession{
			Input: autoscale.Input{
				Replicas: status.Desired, CPUPercent: usage.CPUPercent, MemoryPercent: usage.MemoryPercent,
				Diagnosis: capacity.Diagnosis{Pressure: capacity.PressureNone, Action: capacity.ActionNone, Reason: "K3s workload metrics are within policy"},
			},
			Executor: autoscale.NewExecutor(provider, routeProvider),
		}, nil
	}
	if state.Provider != orchestrator.ProviderSwarm {
		return autoscale.RuntimeSession{}, fmt.Errorf("orchestrator %q does not support active reconciliation", state.Provider)
	}
	provider, err := orchestrator.NewSwarmProviderFromEnv()
	if err != nil {
		return autoscale.RuntimeSession{}, err
	}
	fail := func(err error) (autoscale.RuntimeSession, error) {
		_ = provider.Close()
		return autoscale.RuntimeSession{}, err
	}
	status, err := provider.Status(ctx, deployment)
	if err != nil {
		return fail(err)
	}
	stats, err := docker.GetManagedDeploymentStats(deployment)
	if err != nil {
		return fail(fmt.Errorf("read managed workload statistics: %w", err))
	}
	if status.Available == 0 || len(stats) != status.Available {
		return fail(fmt.Errorf("statistics are available for %d of %d running replicas", len(stats), status.Available))
	}
	hostStats, err := system.GetSystemStats()
	if err != nil {
		return fail(fmt.Errorf("read host capacity: %w", err))
	}
	observations := make([]autoscaleReplicaObservation, 0, len(stats))
	for _, stat := range stats {
		limits, err := docker.GetContainerResources(stat.ContainerID)
		if err != nil {
			return fail(fmt.Errorf("read resources for replica %s: %w", stat.ContainerID, err))
		}
		observations = append(observations, autoscaleReplicaObservation{Stats: stat, Limits: *limits})
	}
	input := autoscaleInput(observations, status, hostStats, f.server.config.Capacity)
	placement, err := provider.Placement(ctx, deployment)
	if err != nil {
		return fail(fmt.Errorf("read managed workload placement: %w", err))
	}
	if usesFleetPlacement(placement) {
		available, err := f.server.fleetCapacityAvailable(ctx, provider, input.CurrentResources)
		if err != nil {
			return fail(fmt.Errorf("read permitted Fleet capacity: %w", err))
		}
		input.FleetOffer = capacity.Offer{Enabled: available}
	}
	routeProvider := routing.NewManagedNginxProvider(f.server.proxyOrchestrator.NginxManager(), f.server.manager, state.Route)
	return autoscale.RuntimeSession{
		Input: input, Executor: autoscale.NewExecutor(provider, routeProvider), Close: provider.Close,
	}, nil
}

func usesFleetPlacement(placement orchestrator.Placement) bool {
	for _, constraint := range placement.Constraints {
		if strings.HasPrefix(constraint, "node.labels.flatrun.capacity.") {
			return true
		}
	}
	return false
}

func autoscaleInput(observations []autoscaleReplicaObservation, status orchestrator.Status, hostStats *system.SystemStats, configPolicy config.CapacityConfig) autoscale.Input {
	var selected capacity.Container
	var cpuPercent float64
	var memoryPercent float64
	var score float64
	for _, observation := range observations {
		stat := observation.Stats
		limits := observation.Limits
		container := capacity.Container{
			ID: stat.ContainerID, Name: stat.Name, CPUPercent: stat.CPUPercent, CPULimit: limits.CPUs,
			MemoryUsage: stat.MemoryUsage, MemoryLimit: uint64(max(limits.MemoryLimit, 0)),
		}
		containerScore := math.Max(stat.CPUPercent, stat.MemoryPercent)
		if selected.ID == "" || containerScore > score {
			selected = container
			score = containerScore
		}
		cpuPercent = math.Max(cpuPercent, stat.CPUPercent)
		memoryPercent = math.Max(memoryPercent, stat.MemoryPercent)
	}
	host := capacity.Host{
		CPUCores: float64(hostStats.CPU.Cores), CPUUsagePercent: hostStats.CPU.UsagePercent,
		MemoryTotal: hostStats.Memory.Total, MemoryAvailable: hostStats.Memory.Available,
	}
	policy := capacity.PolicyFromConfig(configPolicy)
	diagnosis := capacity.Diagnose(host, selected, policy)
	resources := orchestrator.Resources{CPULimit: selected.CPULimit, MemoryLimit: selected.MemoryLimit}
	suggested := resources
	if diagnosis.Action == capacity.ActionIncreaseCPU {
		suggested.CPULimit = diagnosis.RecommendedLimit
	}
	if diagnosis.Action == capacity.ActionIncreaseMemory {
		suggested.MemoryLimit = uint64(diagnosis.RecommendedLimit)
	}
	return autoscale.Input{
		Replicas: status.Desired, CPUPercent: cpuPercent, MemoryPercent: memoryPercent,
		Diagnosis: diagnosis, RequiresFleet: diagnosis.Action == capacity.ActionAddReplica,
		CurrentResources: resources, SuggestedResource: suggested,
	}
}
