package api

import (
	"fmt"
	"log"
	"net/http"

	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

type apiError struct {
	Status int
	Msg    string
}

func (e *apiError) Error() string { return e.Msg }

func apiErrf(status int, format string, args ...interface{}) *apiError {
	return &apiError{Status: status, Msg: fmt.Sprintf(format, args...)}
}

func respondAPIError(c *gin.Context, err error) {
	if ae, ok := err.(*apiError); ok {
		c.JSON(ae.Status, gin.H{"error": ae.Msg})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
}

func cloneMetadata(meta *models.ServiceMetadata) (*models.ServiceMetadata, error) {
	if meta == nil {
		return nil, nil
	}
	data, err := yaml.Marshal(meta)
	if err != nil {
		return nil, err
	}
	var cp models.ServiceMetadata
	if err := yaml.Unmarshal(data, &cp); err != nil {
		return nil, err
	}
	return &cp, nil
}

type deploymentDeleteOptions struct {
	DeleteSSL      bool
	DeleteDatabase bool
	DeleteVhost    bool
}

func (s *Server) applyDeploymentDelete(name string, opts deploymentDeleteOptions) ([]string, error) {
	deployment, _ := s.manager.GetDeployment(name)

	var deletedItems []string

	if opts.DeleteVhost {
		if err := s.proxyOrchestrator.TeardownDeployment(name); err != nil {
			log.Printf("Warning: failed to teardown proxy for %s: %v", name, err)
		} else {
			deletedItems = append(deletedItems, "virtual_host")
		}
	}

	if deployment != nil && deployment.Metadata != nil && opts.DeleteSSL {
		domainsToDelete := deployment.Metadata.GetUniqueDomainNames()
		if len(domainsToDelete) == 0 && deployment.Metadata.Networking.Domain != "" {
			domainsToDelete = []string{deployment.Metadata.Networking.Domain}
		}

		for _, domain := range domainsToDelete {
			if err := s.proxyOrchestrator.SSLManager().DeleteCertificate(domain); err != nil {
				log.Printf("Warning: failed to delete SSL certificate for %s: %v", domain, err)
			} else {
				deletedItems = append(deletedItems, fmt.Sprintf("ssl_certificate:%s", domain))
			}
		}
	}

	if opts.DeleteDatabase && s.config.Infrastructure.Database.Enabled {
		if deployment != nil && deployment.Metadata != nil && len(deployment.Metadata.Databases) > 0 {
			for _, dbConfig := range deployment.Metadata.Databases {
				if dbConfig.IsShared {
					if err := s.deleteDatabaseByAlias(name, dbConfig.Alias); err != nil {
						log.Printf("Warning: failed to delete database %s for %s: %v", dbConfig.Alias, name, err)
					} else {
						deletedItems = append(deletedItems, fmt.Sprintf("database:%s", dbConfig.Alias))
					}
				}
			}
		} else {
			if err := s.deleteDatabaseForDeployment(name); err != nil {
				log.Printf("Warning: failed to delete database for %s: %v", name, err)
			} else {
				deletedItems = append(deletedItems, "database")
			}
		}
	}

	if err := s.manager.DeleteDeployment(name); err != nil {
		return deletedItems, err
	}

	return deletedItems, nil
}

// mutateDomainAdd validates the new domain and appends it to the
// deployment metadata in memory only; persisting is the caller's job.
func (s *Server) mutateDomainAdd(deployment *models.Deployment, domain *models.DomainConfig) error {
	if domain.Domain == "" {
		return apiErrf(http.StatusBadRequest, "Domain is required")
	}

	resolved, err := s.resolveService(deployment.Name, domain.Service)
	if err != nil {
		return apiErrf(http.StatusBadRequest, "%s", err.Error())
	}
	domain.Service = resolved

	if domain.ID == "" {
		domain.ID = generateDomainID()
	}

	if deployment.Metadata == nil {
		deployment.Metadata = &models.ServiceMetadata{}
	}

	if len(deployment.Metadata.Domains) == 0 && deployment.Metadata.Networking.Expose {
		existingService := deployment.Metadata.Networking.Service
		if existingService == "" {
			existingService = resolved
		}
		existingDomain := models.DomainConfig{
			ID:            "default",
			Service:       existingService,
			ContainerPort: deployment.Metadata.Networking.ContainerPort,
			Domain:        deployment.Metadata.Networking.Domain,
			SSL:           deployment.Metadata.SSL,
		}
		deployment.Metadata.Domains = []models.DomainConfig{existingDomain}
	}

	for _, existing := range deployment.Metadata.Domains {
		if existing.Domain == domain.Domain && existing.PathPrefix == domain.PathPrefix {
			return apiErrf(http.StatusConflict, "Domain %s%s already exists", domain.Domain, domain.PathPrefix)
		}
	}

	if domain.ContainerPort == 0 && deployment.Metadata.Networking.ContainerPort != 0 {
		domain.ContainerPort = deployment.Metadata.Networking.ContainerPort
	}

	deployment.Metadata.Domains = append(deployment.Metadata.Domains, *domain)
	return nil
}

// mutateDomainUpdate replaces the domain with the given ID in memory
// only; persisting is the caller's job.
func (s *Server) mutateDomainUpdate(deployment *models.Deployment, domainID string, updated *models.DomainConfig) error {
	if deployment.Metadata == nil || len(deployment.Metadata.Domains) == 0 {
		return apiErrf(http.StatusNotFound, "Domain not found")
	}

	if updated.Service != "" {
		resolved, err := s.resolveService(deployment.Name, updated.Service)
		if err != nil {
			return apiErrf(http.StatusBadRequest, "%s", err.Error())
		}
		updated.Service = resolved
	}

	for i, d := range deployment.Metadata.Domains {
		if d.ID == domainID {
			updated.ID = domainID
			if updated.Service == "" {
				updated.Service = d.Service
			}
			deployment.Metadata.Domains[i] = *updated
			return nil
		}
	}
	return apiErrf(http.StatusNotFound, "Domain not found")
}

// mutateDomainDelete removes the domain with the given ID in memory and
// reports whether the proxy should be torn down (true) or re-rendered
// (false). Persisting is the caller's job.
func mutateDomainDelete(deployment *models.Deployment, domainID string) (bool, error) {
	meta := deployment.Metadata
	if meta == nil {
		return false, apiErrf(http.StatusNotFound, "Domain not found")
	}

	// Legacy "default" domain backed by the networking config rather
	// than the domains list.
	if domainID == "default" && len(meta.Domains) == 0 {
		if !meta.Networking.Expose || meta.Networking.Domain == "" {
			return false, apiErrf(http.StatusNotFound, "Domain not found")
		}
		meta.Networking.Expose = false
		meta.Networking.Domain = ""
		meta.SSL.Enabled = false
		meta.SSL.AutoCert = false
		return true, nil
	}

	if len(meta.Domains) == 0 {
		return false, apiErrf(http.StatusNotFound, "Domain not found")
	}

	found := false
	newDomains := make([]models.DomainConfig, 0)
	for _, d := range meta.Domains {
		if d.ID == domainID {
			found = true
			continue
		}
		newDomains = append(newDomains, d)
	}
	if !found {
		return false, apiErrf(http.StatusNotFound, "Domain not found")
	}

	if len(newDomains) == 0 {
		meta.Domains = nil
		return !meta.Networking.Expose, nil
	}
	meta.Domains = newDomains
	return false, nil
}
