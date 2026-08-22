package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flatrun/agent/internal/autoscale"
	"github.com/flatrun/agent/internal/cluster"
	"github.com/flatrun/agent/internal/orchestrator"
	"github.com/flatrun/agent/internal/routing"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

type autoscalePolicyRequest struct {
	Enabled            bool    `json:"enabled"`
	MinReplicas        int     `json:"min_replicas"`
	MaxReplicas        int     `json:"max_replicas"`
	ScaleUpPercent     float64 `json:"scale_up_percent"`
	ScaleDownPercent   float64 `json:"scale_down_percent"`
	ScaleUpWindows     int     `json:"scale_up_windows"`
	ScaleDownWindows   int     `json:"scale_down_windows"`
	CooldownSeconds    int64   `json:"cooldown_seconds"`
	AllowFleetCapacity bool    `json:"allow_fleet_capacity"`
}

func (s *Server) activateDeploymentAutoscale(c *gin.Context) {
	if s.runAutoscaleActivation == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Autoscaling activation is unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 3*time.Minute)
	defer cancel()
	activation, err := s.runAutoscaleActivation(ctx, c.Param("name"))
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, activation)
}

func (s *Server) defaultRunAutoscaleActivation(ctx context.Context, name string) (autoscale.Activation, error) {
	if s.autoscaleStore == nil {
		return autoscale.Activation{}, fmt.Errorf("Autoscaling storage is unavailable")
	}
	orchestratorID := orchestrator.ProviderID(s.config.Cluster.Orchestrator)
	routingID := routing.ProviderID(s.config.Cluster.Routing)
	if routingID == "" {
		routingID = routing.ProviderNginx
	}
	if orchestratorID != orchestrator.ProviderSwarm && orchestratorID != orchestrator.ProviderK3s {
		return autoscale.Activation{}, fmt.Errorf("Autoscaling activation requires Swarm or K3s")
	}
	if (orchestratorID == orchestrator.ProviderSwarm && routingID != routing.ProviderNginx) ||
		(orchestratorID == orchestrator.ProviderK3s && routingID != routing.ProviderTraefik) {
		return autoscale.Activation{}, fmt.Errorf("The selected orchestrator and routing providers are incompatible")
	}
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		return autoscale.Activation{}, fmt.Errorf("Deployment not found")
	}
	composeContent, _, err := s.manager.GetComposeFile(name)
	if err != nil {
		return autoscale.Activation{}, fmt.Errorf("Compose configuration is unavailable")
	}
	policy, err := s.autoscaleStore.Policy(name)
	if err != nil {
		return autoscale.Activation{}, err
	}
	workload, err := autoscale.BuildWorkload(deployment, composeContent, policy.MinReplicas, s.config.Infrastructure.DefaultProxyNetwork)
	if err != nil {
		return autoscale.Activation{}, err
	}
	domain, err := autoscaleDomain(deployment, workload)
	if err != nil {
		return autoscale.Activation{}, err
	}
	var orchestratorProvider orchestrator.Provider
	var routeProvider routing.Provider
	if orchestratorID == orchestrator.ProviderSwarm {
		swarmProvider, err := orchestrator.NewSwarmProviderFromEnv()
		if err != nil {
			return autoscale.Activation{}, fmt.Errorf("create Swarm provider: %w", err)
		}
		defer swarmProvider.Close()
		workload.Placement, err = s.autoscalePlacement(ctx, swarmProvider, policy, workload.Resources)
		if err != nil {
			return autoscale.Activation{}, err
		}
		orchestratorProvider = swarmProvider
		routeProvider = routing.NewManagedNginxProvider(s.proxyOrchestrator.NginxManager(), s.manager)
	} else {
		orchestratorProvider = orchestrator.NewK3sProvider(s.config.Cluster.K3s.Kubeconfig, s.config.Cluster.K3s.Namespace)
		routeProvider = routing.NewK3sIngressProvider(s.config.Cluster.K3s.Kubeconfig, s.config.Cluster.K3s.Namespace)
	}
	stopper := autoscale.ServiceStopperFunc(func(deployment, service string) (string, error) {
		return s.manager.StopService(deployment, service)
	})
	previousState, err := s.autoscaleStore.State(name)
	if err != nil {
		return autoscale.Activation{}, err
	}
	persisted := false
	activation, err := autoscale.NewActivator(orchestratorProvider, routeProvider, stopper).ActivateDurably(ctx, name, deployment.Metadata.Scaling.Service, workload, routing.Route{
		ID: name, Service: deployment.Metadata.Scaling.Service, Domain: domain.Domain, Path: domain.PathPrefix, Protocol: "http",
	}, func(activation autoscale.Activation) error {
		state := previousState
		state.Active = true
		state.Provider = orchestratorID
		state.Service = deployment.Metadata.Scaling.Service
		state.Replicas = activation.Workload.Desired
		state.Route = activation.Route
		state.LastAction = time.Now()
		if err := s.autoscaleStore.SetState(name, state); err != nil {
			return err
		}
		persisted = true
		return nil
	})
	if err != nil {
		if persisted {
			if restoreErr := s.autoscaleStore.SetState(name, previousState); restoreErr != nil {
				return autoscale.Activation{}, fmt.Errorf("%v; restore autoscaling state: %w", err, restoreErr)
			}
		}
		return autoscale.Activation{}, err
	}
	return activation, nil
}

func (s *Server) autoscalePlacement(ctx context.Context, provider *orchestrator.SwarmProvider, policy autoscale.Policy, resources orchestrator.Resources) (orchestrator.Placement, error) {
	identity, err := provider.EnsureLocalNodeLabel(ctx, "flatrun.capacity.local", "true")
	if err != nil {
		return orchestrator.Placement{}, err
	}
	local := orchestrator.Placement{Constraints: []string{"node.hostname==" + identity.Hostname}}
	manager := s.getClusterManager()
	if !policy.AllowFleetCapacity || manager == nil {
		return local, nil
	}
	if resources.CPULimit == 0 || resources.MemoryLimit == 0 {
		return orchestrator.Placement{}, fmt.Errorf("Fleet capacity requires CPU and memory limits in the Compose deployment resources")
	}
	label := capacityNodeLabel(manager.ServerName())
	if _, err := provider.EnsureLocalNodeLabel(ctx, label, "true"); err != nil {
		return orchestrator.Placement{}, err
	}
	claims := manager.ForEachPeer(ctx, func(ctx context.Context, _ string, client *cluster.Client) ([]byte, error) {
		data, status, err := client.Post(ctx, "/api/cluster/capacity/claim", nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("capacity claim returned status %d", status)
		}
		return data, nil
	})
	allowed := 0
	incompatible := 0
	maxReplicas := 0
	constraint := "node.labels." + label + "==true"
	for _, result := range claims {
		if result.Error != "" {
			continue
		}
		var claim clusterCapacityClaimResponse
		if err := json.Unmarshal(result.Data, &claim); err != nil || !claim.Enabled || claim.Constraint != constraint {
			continue
		}
		if claim.Node.ClusterID != identity.ClusterID {
			incompatible++
			continue
		}
		if !capacityClaimFits(claim, resources, identity.ClusterID) {
			continue
		}
		allowed++
		if claim.MaxReplicas > 0 && (maxReplicas == 0 || claim.MaxReplicas < maxReplicas) {
			maxReplicas = claim.MaxReplicas
		}
	}
	if allowed == 0 {
		if incompatible > 0 {
			return orchestrator.Placement{}, fmt.Errorf("Permitted Fleet servers must join the same Docker Swarm before they can lend capacity")
		}
		return local, nil
	}
	return orchestrator.Placement{Constraints: []string{constraint}, MaxReplicasPerNode: uint64(maxReplicas)}, nil
}

func capacityClaimFits(claim clusterCapacityClaimResponse, resources orchestrator.Resources, clusterID string) bool {
	return clusterID != "" && claim.Node.ClusterID == clusterID &&
		(claim.MaxCPU == 0 || resources.CPULimit <= claim.MaxCPU) &&
		(claim.MaxMemory == 0 || resources.MemoryLimit <= claim.MaxMemory)
}

func (s *Server) fleetCapacityAvailable(ctx context.Context, provider *orchestrator.SwarmProvider, resources orchestrator.Resources) (bool, error) {
	manager := s.getClusterManager()
	if manager == nil {
		return false, nil
	}
	identity, err := provider.LocalNodeIdentity(ctx)
	if err != nil {
		return false, err
	}
	constraint := "node.labels." + capacityNodeLabel(manager.ServerName()) + "==true"
	claims := manager.ForEachPeer(ctx, func(ctx context.Context, _ string, client *cluster.Client) ([]byte, error) {
		data, status, err := client.Post(ctx, "/api/cluster/capacity/claim", nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("capacity claim returned status %d", status)
		}
		return data, nil
	})
	for _, result := range claims {
		if result.Error != "" {
			continue
		}
		var claim clusterCapacityClaimResponse
		if err := json.Unmarshal(result.Data, &claim); err == nil && claim.Enabled && claim.Constraint == constraint && capacityClaimFits(claim, resources, identity.ClusterID) {
			return true, nil
		}
	}
	return false, nil
}

func autoscaleDomain(deployment *models.Deployment, workload orchestrator.Workload) (models.DomainConfig, error) {
	if deployment.Metadata == nil {
		return models.DomainConfig{}, fmt.Errorf("Scale-ready service must have an exposed domain")
	}
	service := deployment.Metadata.Scaling.Service
	for _, domain := range deployment.Metadata.GetDomains() {
		if domain.Service == service && strings.TrimSpace(domain.Domain) != "" && domain.ContainerPort == workload.Port {
			return domain, nil
		}
	}
	return models.DomainConfig{}, fmt.Errorf("Scale-ready service must have an exposed domain")
}

type autoscalePolicyResponse struct {
	autoscalePolicyRequest
	State autoscale.State `json:"state"`
}

func (s *Server) getDeploymentAutoscalePolicy(c *gin.Context) {
	if s.autoscaleStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Autoscaling storage is unavailable"})
		return
	}
	name := c.Param("name")
	if _, err := s.manager.GetDeployment(name); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}
	policy, err := s.autoscaleStore.Policy(name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	state, err := s.autoscaleStore.State(name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, autoscalePolicyResponse{autoscalePolicyRequest: policyRequest(policy), State: state})
}

func (s *Server) getDeploymentAutoscaleCompatibility(c *gin.Context) {
	s.writeAutoscaleCompatibility(c, c.Param("name"))
}

func (s *Server) writeAutoscaleCompatibility(c *gin.Context, name string) {
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}
	composeContent, _, err := s.manager.GetComposeFile(name)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "Compose configuration is unavailable"})
		return
	}
	c.JSON(http.StatusOK, autoscale.AssessCompatibility(deployment, composeContent))
}

func (s *Server) updateDeploymentAutoscaleWorkload(c *gin.Context) {
	name := c.Param("name")
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}
	var scaling models.ScalingConfig
	if err := c.ShouldBindJSON(&scaling); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if deployment.Metadata == nil {
		deployment.Metadata = &models.ServiceMetadata{Name: name}
	}
	deployment.Metadata.Scaling = &scaling
	if err := s.manager.SaveMetadata(name, deployment.Metadata); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	s.writeAutoscaleCompatibility(c, name)
}

func (s *Server) updateDeploymentAutoscalePolicy(c *gin.Context) {
	if s.autoscaleStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Autoscaling storage is unavailable"})
		return
	}
	name := c.Param("name")
	if _, err := s.manager.GetDeployment(name); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}
	var req autoscalePolicyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	policy := autoscale.Policy{
		Enabled: req.Enabled, MinReplicas: req.MinReplicas, MaxReplicas: req.MaxReplicas,
		ScaleUpPercent: req.ScaleUpPercent, ScaleDownPercent: req.ScaleDownPercent,
		ScaleUpWindows: req.ScaleUpWindows, ScaleDownWindows: req.ScaleDownWindows,
		Cooldown: time.Duration(req.CooldownSeconds) * time.Second, AllowFleetCapacity: req.AllowFleetCapacity,
	}
	if err := s.autoscaleStore.SetPolicy(name, policy); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	state, err := s.autoscaleStore.State(name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, autoscalePolicyResponse{autoscalePolicyRequest: req, State: state})
}

func policyRequest(policy autoscale.Policy) autoscalePolicyRequest {
	return autoscalePolicyRequest{
		Enabled: policy.Enabled, MinReplicas: policy.MinReplicas, MaxReplicas: policy.MaxReplicas,
		ScaleUpPercent: policy.ScaleUpPercent, ScaleDownPercent: policy.ScaleDownPercent,
		ScaleUpWindows: policy.ScaleUpWindows, ScaleDownWindows: policy.ScaleDownWindows,
		CooldownSeconds: int64(policy.Cooldown / time.Second), AllowFleetCapacity: policy.AllowFleetCapacity,
	}
}
