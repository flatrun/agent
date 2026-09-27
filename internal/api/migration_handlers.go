package api

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

type migrationStatus struct {
	Plan            *models.MigrationSpec `json:"plan"`
	RetirementReady bool                  `json:"retirement_ready"`
	Blockers        []string              `json:"blockers"`
}

func (s *Server) getDeploymentMigration(c *gin.Context) {
	deployment, err := s.manager.GetDeployment(c.Param("name"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}
	var plan *models.MigrationSpec
	if deployment.Metadata != nil {
		plan = deployment.Metadata.Migration
	}
	c.JSON(http.StatusOK, gin.H{"migration": buildMigrationStatus(plan)})
}

func (s *Server) updateDeploymentMigration(c *gin.Context) {
	name := c.Param("name")
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}
	var plan models.MigrationSpec
	if err := c.ShouldBindJSON(&plan); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	seen := map[string]bool{}
	for _, site := range plan.Sites {
		hostname := strings.TrimSpace(strings.ToLower(site.Hostname))
		if hostname == "" || seen[hostname] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Migration hostnames must be present and unique"})
			return
		}
		seen[hostname] = true
	}
	if deployment.Metadata == nil {
		deployment.Metadata = &models.ServiceMetadata{Name: name}
	}
	deployment.Metadata.Migration = &plan
	if err := s.manager.SaveMetadata(name, deployment.Metadata); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"migration": buildMigrationStatus(&plan)})
}

func (s *Server) checkDeploymentMigrationDNS(c *gin.Context) {
	name := c.Param("name")
	deployment, err := s.manager.GetDeployment(name)
	if err != nil || deployment.Metadata == nil || deployment.Metadata.Migration == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Migration plan not found"})
		return
	}
	plan := deployment.Metadata.Migration
	for i := range plan.Sites {
		addresses, lookupErr := net.LookupHost(plan.Sites[i].Hostname)
		if lookupErr != nil {
			addresses = []string{}
		}
		plan.Sites[i].Resolved = addresses
		plan.Sites[i].DNSPropagated = containsString(addresses, plan.ExpectedAddress)
	}
	if err := s.manager.SaveMetadata(name, deployment.Metadata); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"migration": buildMigrationStatus(plan)})
}

func containsString(values []string, wanted string) bool {
	if wanted == "" {
		return false
	}
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func buildMigrationStatus(plan *models.MigrationSpec) migrationStatus {
	status := migrationStatus{Plan: plan, Blockers: []string{}}
	if plan == nil {
		status.Blockers = append(status.Blockers, "migration plan is missing")
		return status
	}
	if !plan.InventoryComplete {
		status.Blockers = append(status.Blockers, "source inventory is incomplete")
	}
	for _, site := range plan.Sites {
		if !site.Transferred {
			status.Blockers = append(status.Blockers, site.Hostname+" has not completed its initial transfer")
		}
		if !site.DNSPropagated && plan.CutoverAt != nil {
			status.Blockers = append(status.Blockers, site.Hostname+" DNS has not propagated")
		}
	}
	if plan.LastSyncAt == nil || time.Since(*plan.LastSyncAt) > 15*time.Minute {
		status.Blockers = append(status.Blockers, "a recent final synchronization is required")
	}
	if plan.CutoverAt == nil {
		status.Blockers = append(status.Blockers, "cutover has not been recorded")
	}
	status.RetirementReady = len(status.Blockers) == 0
	return status
}
