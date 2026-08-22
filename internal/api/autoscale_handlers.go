package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flatrun/agent/internal/autoscale"
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
	activation, err := s.runAutoscaleActivation(c, c.Param("name"))
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
	if s.config.Cluster.Orchestrator != string(orchestrator.ProviderSwarm) {
		return autoscale.Activation{}, fmt.Errorf("Autoscaling activation requires the Swarm orchestrator")
	}
	if s.config.Cluster.Routing != "" && s.config.Cluster.Routing != string(routing.ProviderNginx) {
		return autoscale.Activation{}, fmt.Errorf("Autoscaling activation requires Nginx routing")
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
	swarmProvider, err := orchestrator.NewSwarmProviderFromEnv()
	if err != nil {
		return autoscale.Activation{}, fmt.Errorf("create Swarm provider: %w", err)
	}
	defer swarmProvider.Close()
	routeProvider := routing.NewManagedNginxProvider(s.proxyOrchestrator.NginxManager(), s.manager)
	stopper := autoscale.ServiceStopperFunc(func(deployment, service string) (string, error) {
		return s.manager.StopService(deployment, service)
	})
	activation, err := autoscale.NewActivator(swarmProvider, routeProvider, stopper).Activate(ctx, name, deployment.Metadata.Scaling.Service, workload, routing.Route{
		ID: name, Service: deployment.Metadata.Scaling.Service, Domain: domain.Domain, Path: domain.PathPrefix, Protocol: "http",
	})
	if err != nil {
		return autoscale.Activation{}, err
	}
	state, err := s.autoscaleStore.State(name)
	if err != nil {
		return autoscale.Activation{}, err
	}
	state.Active = true
	state.Provider = orchestrator.ProviderSwarm
	state.Service = deployment.Metadata.Scaling.Service
	state.Replicas = activation.Workload.Desired
	state.LastAction = time.Now()
	if err := s.autoscaleStore.SetState(name, state); err != nil {
		return autoscale.Activation{}, err
	}
	return activation, nil
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
