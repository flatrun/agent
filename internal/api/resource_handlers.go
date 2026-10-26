package api

import (
	"net/http"
	"strconv"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/capacity"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/system"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

func (s *Server) getCapacityStatus(c *gin.Context) {
	hostStats, err := system.GetSystemStats()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	host := capacity.Host{
		CPUCores:        float64(hostStats.CPU.Cores),
		CPUUsagePercent: hostStats.CPU.UsagePercent,
		MemoryTotal:     hostStats.Memory.Total,
		MemoryAvailable: hostStats.Memory.Available,
	}
	policy := capacity.PolicyFromConfig(s.config.Capacity)
	c.JSON(http.StatusOK, gin.H{
		"host":   host,
		"policy": policy,
		"offer":  capacity.FleetOffer(host, policy, s.config.Capacity.OfferToFleet),
	})
}

func (s *Server) getContainerResources(c *gin.Context) {
	id := c.Param("id")
	if !s.requireContainerAccess(c, id, auth.AccessLevelRead) {
		return
	}

	resources, err := docker.GetContainerResources(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"resources": resources,
	})
}

func (s *Server) diagnoseContainerCapacity(c *gin.Context) {
	id := c.Param("id")
	if !s.requireContainerAccess(c, id, auth.AccessLevelRead) {
		return
	}

	stats, err := docker.GetContainerStats(id)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	limits, err := docker.GetContainerResources(id)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	hostStats, err := system.GetSystemStats()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	host := capacity.Host{
		CPUCores:        float64(hostStats.CPU.Cores),
		CPUUsagePercent: hostStats.CPU.UsagePercent,
		MemoryTotal:     hostStats.Memory.Total,
		MemoryAvailable: hostStats.Memory.Available,
	}
	container := capacity.Container{
		ID:          stats.ContainerID,
		Name:        stats.Name,
		CPUPercent:  stats.CPUPercent,
		CPULimit:    limits.CPUs,
		MemoryUsage: stats.MemoryUsage,
		MemoryLimit: uint64(max(limits.MemoryLimit, 0)),
	}
	policy := capacity.PolicyFromConfig(s.config.Capacity)
	c.JSON(http.StatusOK, gin.H{
		"host":      host,
		"container": container,
		"policy":    policy,
		"diagnosis": capacity.Diagnose(host, container, policy),
	})
}

func (s *Server) updateContainerResources(c *gin.Context) {
	id := c.Param("id")

	var update docker.ResourceUpdate
	if err := c.ShouldBindJSON(&update); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid request body: " + err.Error(),
		})
		return
	}

	if update.MemoryLimit == nil && update.MemorySwap == nil &&
		update.CPUs == nil && update.CPUShares == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "At least one resource limit must be specified",
		})
		return
	}

	if !s.requireContainerAccess(c, id, auth.AccessLevelWrite) {
		return
	}

	if err := docker.UpdateContainerResources(id, &update); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	resources, _ := docker.GetContainerResources(id)

	response := gin.H{
		"message":   "Resources updated",
		"resources": resources,
	}
	if persisted, err := s.persistContainerResources(id, &update); err != nil {
		response["persisted"] = false
		response["persist_error"] = err.Error()
	} else {
		response["persisted"] = persisted
	}
	c.JSON(http.StatusOK, response)
}

// persistContainerResources saves CPU and memory limits set on a
// deployment's container into its service settings, so recreating the
// container keeps them. It reports false for a container outside any
// deployment, where there is nothing to save them in.
func (s *Server) persistContainerResources(containerID string, update *docker.ResourceUpdate) (bool, error) {
	if update.CPUs == nil && update.MemoryLimit == nil {
		return false, nil
	}
	name, err := s.containerDeploymentName(containerID)
	if err != nil || name == "" {
		return false, nil
	}
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		return false, nil
	}
	service := ""
	for _, svc := range deployment.Services {
		if deploymentContainsContainer(&models.Deployment{Services: []models.Service{svc}}, containerID) {
			service = svc.Name
			break
		}
	}
	if service == "" {
		return false, nil
	}

	settings := models.ServiceSettings{}
	if deployment.Metadata != nil {
		settings = deployment.Metadata.ServiceSettings[service]
	}
	resources := models.ServiceResources{}
	if settings.Resources != nil {
		resources = *settings.Resources
	}
	if update.CPUs != nil {
		resources.CPUs = ""
		if *update.CPUs > 0 {
			resources.CPUs = strconv.FormatFloat(*update.CPUs, 'f', -1, 64)
		}
	}
	if update.MemoryLimit != nil {
		resources.Memory = composeMemory(*update.MemoryLimit)
	}
	settings.Resources = &resources
	if err := s.commitServiceSettings(name, service, &settings); err != nil {
		return false, err
	}
	return true, nil
}

func composeMemory(bytes int64) string {
	switch {
	case bytes <= 0:
		return ""
	case bytes%(1<<20) == 0:
		return strconv.FormatInt(bytes>>20, 10) + "m"
	default:
		return strconv.FormatInt(bytes, 10) + "b"
	}
}

func (s *Server) getDeploymentResources(c *gin.Context) {
	name := c.Param("name")

	if _, err := s.manager.GetDeployment(name); err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "deployment not found",
		})
		return
	}

	resources, err := docker.GetDeploymentResources(name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"deployment": name,
		"resources":  resources,
	})
}
