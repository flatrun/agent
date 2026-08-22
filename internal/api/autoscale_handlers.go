package api

import (
	"net/http"
	"time"

	"github.com/flatrun/agent/internal/autoscale"
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
	name := c.Param("name")
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
