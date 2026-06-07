package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/flatrun/agent/internal/plan"
	"github.com/flatrun/agent/internal/proxy"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

func renderEnvContent(envVars []EnvVar) string {
	var content strings.Builder
	for _, env := range envVars {
		if env.Key != "" {
			content.WriteString(fmt.Sprintf("%s=%s\n", env.Key, env.Value))
		}
	}
	return content.String()
}

func diffEnvCounts(current, requested []EnvVar) (added, changed, removed int) {
	currentMap := make(map[string]string, len(current))
	for _, e := range current {
		currentMap[e.Key] = e.Value
	}
	requestedKeys := make(map[string]struct{}, len(requested))
	for _, e := range requested {
		if e.Key == "" {
			continue
		}
		requestedKeys[e.Key] = struct{}{}
		old, ok := currentMap[e.Key]
		switch {
		case !ok:
			added++
		case old != e.Value:
			changed++
		}
	}
	for _, e := range current {
		if _, ok := requestedKeys[e.Key]; !ok {
			removed++
		}
	}
	return added, changed, removed
}

func (s *Server) runningContainerReplacements(name, reason string) []plan.Change {
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		return nil
	}
	var changes []plan.Change
	for _, svc := range deployment.Services {
		if svc.Status != "running" {
			continue
		}
		changes = append(changes, plan.Change{
			Type:    "container",
			ID:      svc.Name,
			Actions: []string{plan.ActionDelete, plan.ActionCreate},
			Reason:  reason,
		})
	}
	return changes
}

func (s *Server) vhostConfigPath(name string) string {
	return filepath.Join(s.proxyOrchestrator.NginxManager().ConfigPath(), name+".conf")
}

func (s *Server) planEnvUpdate(c *gin.Context, name string, envVars []EnvVar) {
	envRel := filepath.Join(name, ".env.flatrun")
	beforeBytes, readErr := os.ReadFile(filepath.Join(s.config.DeploymentsPath, envRel))
	exists := readErr == nil
	before := string(beforeBytes)
	after := renderEnvContent(envVars)

	p := s.newPlan("deployment.env.update", "deployment", name)
	p.Snapshot.Files = plan.SnapshotFiles(s.config.DeploymentsPath, envRel)

	if exists && before == after {
		p.Changes = append(p.Changes, plan.Change{
			Type: "file", ID: ".env.flatrun",
			Actions:   []string{plan.ActionNoOp},
			Reason:    "rendered content is identical to the current file",
			Sensitive: true,
		})
	} else {
		action := plan.ActionCreate
		var beforePtr *string
		if exists {
			action = plan.ActionUpdate
			beforePtr = plan.StrPtr(before)
		}
		added, changed, removed := diffEnvCounts(parseEnvContent(before), envVars)
		p.Changes = append(p.Changes, plan.Change{
			Type: "file", ID: ".env.flatrun",
			Actions:   []string{action},
			Reason:    fmt.Sprintf("%d variable(s) added, %d changed, %d removed", added, changed, removed),
			Before:    beforePtr,
			After:     plan.StrPtr(after),
			Sensitive: true,
		})
		p.Changes = append(p.Changes, s.runningContainerReplacements(name,
			"env file change requires recreating containers; takes effect on the next start or deploy, not on apply")...)
	}

	s.finishPlan(c, p, gin.H{"env_vars": envVars})
}

func applyPlannedEnvUpdate(s *Server, p *plan.Plan) (gin.H, error) {
	var req struct {
		EnvVars []EnvVar `json:"env_vars"`
	}
	if err := json.Unmarshal(p.Request.Body, &req); err != nil {
		return nil, apiErrf(http.StatusBadRequest, "invalid plan body: %s", err.Error())
	}
	if err := s.writeEnvFile(p.Resource.ID, req.EnvVars); err != nil {
		return nil, err
	}
	return gin.H{"message": "Environment variables updated"}, nil
}

func (s *Server) planComposeUpdate(c *gin.Context, name, content string) {
	current, filename, err := s.manager.GetComposeFile(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}
	rel := filepath.Join(name, filename)

	p := s.newPlan("deployment.compose.update", "deployment", name)
	p.Snapshot.Files = plan.SnapshotFiles(s.config.DeploymentsPath, rel)

	if current == content {
		p.Changes = append(p.Changes, plan.Change{
			Type: "file", ID: filename,
			Actions: []string{plan.ActionNoOp},
			Reason:  "submitted compose file is identical to the current one",
		})
	} else {
		p.Changes = append(p.Changes, plan.Change{
			Type: "file", ID: filename,
			Actions: []string{plan.ActionUpdate},
			Reason:  "compose configuration replaced with the submitted content",
			Before:  plan.StrPtr(current),
			After:   plan.StrPtr(content),
		})
		p.Changes = append(p.Changes, s.runningContainerReplacements(name,
			"compose change requires recreating containers; takes effect on the next deploy, not on apply")...)
	}

	s.finishPlan(c, p, gin.H{"compose_content": content})
}

func applyPlannedComposeUpdate(s *Server, p *plan.Plan) (gin.H, error) {
	var req struct {
		ComposeContent string `json:"compose_content"`
	}
	if err := json.Unmarshal(p.Request.Body, &req); err != nil {
		return nil, apiErrf(http.StatusBadRequest, "invalid plan body: %s", err.Error())
	}
	if err := s.validateComposeContent(req.ComposeContent, p.Resource.ID); err != nil {
		return nil, apiErrf(http.StatusBadRequest, "%s", err.Error())
	}
	if err := s.manager.UpdateDeployment(p.Resource.ID, req.ComposeContent); err != nil {
		return nil, err
	}
	return gin.H{"message": "Deployment updated", "name": p.Resource.ID}, nil
}

func (s *Server) planDeploymentDelete(c *gin.Context, name string, opts deploymentDeleteOptions) {
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}

	p := s.newPlan("deployment.delete", "deployment", name)

	snapshotPaths := []string{filepath.Join(name, "service.yml")}
	if _, composeName, cerr := s.manager.GetComposeFile(name); cerr == nil {
		snapshotPaths = append(snapshotPaths, filepath.Join(name, composeName))
	}
	vhostPath := s.vhostConfigPath(name)
	snapshotPaths = append(snapshotPaths, vhostPath)
	p.Snapshot.Files = plan.SnapshotFiles(s.config.DeploymentsPath, snapshotPaths...)

	p.Changes = append(p.Changes, plan.Change{
		Type: "file", ID: name + "/",
		Actions: []string{plan.ActionDelete},
		Reason:  "deployment directory is removed, including all configs and bind-mounted data",
	})
	for _, svc := range deployment.Services {
		p.Changes = append(p.Changes, plan.Change{
			Type: "container", ID: svc.Name,
			Actions: []string{plan.ActionDelete},
			Reason:  "containers are stopped and removed with the deployment",
		})
	}
	nginxMgr := s.proxyOrchestrator.NginxManager()
	if opts.DeleteVhost && nginxMgr.VirtualHostExists(name) {
		var beforePtr *string
		if current, verr := nginxMgr.GetVirtualHost(name); verr == nil {
			beforePtr = plan.StrPtr(current)
		}
		p.Changes = append(p.Changes, plan.Change{
			Type: "file", ID: name + ".conf",
			Actions: []string{plan.ActionDelete},
			Reason:  "reverse proxy virtual host is removed",
			Before:  beforePtr,
		})
	}
	if opts.DeleteSSL && deployment.Metadata != nil {
		domains := deployment.Metadata.GetUniqueDomainNames()
		if len(domains) == 0 && deployment.Metadata.Networking.Domain != "" {
			domains = []string{deployment.Metadata.Networking.Domain}
		}
		for _, domain := range domains {
			if s.proxyOrchestrator.SSLManager().CertificateExists(domain) {
				p.Changes = append(p.Changes, plan.Change{
					Type: "certificate", ID: domain,
					Actions: []string{plan.ActionDelete},
					Reason:  "SSL certificate is deleted with the deployment",
				})
			}
		}
	}
	if opts.DeleteDatabase && s.config.Infrastructure.Database.Enabled {
		if deployment.Metadata != nil && len(deployment.Metadata.Databases) > 0 {
			for _, dbConfig := range deployment.Metadata.Databases {
				if dbConfig.IsShared {
					p.Changes = append(p.Changes, plan.Change{
						Type: "database", ID: dbConfig.Alias,
						Actions: []string{plan.ActionDelete},
						Reason:  "shared database and its user are dropped",
					})
				}
			}
		} else {
			p.Changes = append(p.Changes, plan.Change{
				Type: "database", ID: "primary",
				Actions: []string{plan.ActionDelete},
				Reason:  "shared database and its user are dropped",
			})
		}
	}

	s.finishPlan(c, p, gin.H{
		"delete_ssl":      opts.DeleteSSL,
		"delete_database": opts.DeleteDatabase,
		"delete_vhost":    opts.DeleteVhost,
	})
}

func applyPlannedDeploymentDelete(s *Server, p *plan.Plan) (gin.H, error) {
	var opts struct {
		DeleteSSL      bool `json:"delete_ssl"`
		DeleteDatabase bool `json:"delete_database"`
		DeleteVhost    bool `json:"delete_vhost"`
	}
	if err := json.Unmarshal(p.Request.Body, &opts); err != nil {
		return nil, apiErrf(http.StatusBadRequest, "invalid plan body: %s", err.Error())
	}
	deletedItems, err := s.applyDeploymentDelete(p.Resource.ID, deploymentDeleteOptions{
		DeleteSSL:      opts.DeleteSSL,
		DeleteDatabase: opts.DeleteDatabase,
		DeleteVhost:    opts.DeleteVhost,
	})
	if err != nil {
		return nil, err
	}
	return gin.H{"message": "Deployment deleted", "name": p.Resource.ID, "deleted_items": deletedItems}, nil
}

// planDomainChange simulates a domain mutation on a metadata clone and
// previews the resulting service.yml and virtual host.
func (s *Server) planDomainChange(c *gin.Context, deployment *models.Deployment, action string, body interface{}, mutate func(*models.Deployment) (bool, error)) {
	name := deployment.Name

	metaRel := filepath.Join(name, "service.yml")
	metaBytes, metaErr := os.ReadFile(filepath.Join(s.config.DeploymentsPath, metaRel))
	vhostCurrent, vhostErr := s.proxyOrchestrator.NginxManager().GetVirtualHost(name)

	metaCopy, err := cloneMetadata(deployment.Metadata)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	depCopy := *deployment
	depCopy.Metadata = metaCopy

	teardown, err := mutate(&depCopy)
	if err != nil {
		respondAPIError(c, err)
		return
	}

	p := s.newPlan(action, "deployment", name)
	p.Snapshot.Files = plan.SnapshotFiles(s.config.DeploymentsPath, metaRel, s.vhostConfigPath(name))

	afterMeta, err := yaml.Marshal(depCopy.Metadata)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	metaChange := plan.Change{
		Type: "file", ID: "service.yml",
		Actions: []string{plan.ActionUpdate},
		Reason:  "deployment metadata updated with the domain change",
		After:   plan.StrPtr(string(afterMeta)),
	}
	if metaErr != nil {
		metaChange.Actions = []string{plan.ActionCreate}
		metaChange.Reason = "deployment metadata file is created for the domain change"
	} else {
		metaChange.Before = plan.StrPtr(string(metaBytes))
	}
	p.Changes = append(p.Changes, metaChange)

	if teardown {
		if vhostErr == nil {
			p.Changes = append(p.Changes, plan.Change{
				Type: "file", ID: name + ".conf",
				Actions: []string{plan.ActionDelete},
				Reason:  "deployment is no longer exposed; virtual host is removed",
				Before:  plan.StrPtr(vhostCurrent),
			})
		}
	} else {
		rendered, rerr := s.proxyOrchestrator.RenderDeployment(&depCopy)
		switch {
		case rerr != nil:
			p.Changes = append(p.Changes, plan.Change{
				Type: "file", ID: name + ".conf",
				Actions: []string{plan.ActionUpdate},
				Reason:  "virtual host will be regenerated (preview unavailable: " + rerr.Error() + ")",
			})
		case rendered == "":
		case vhostErr != nil:
			p.Changes = append(p.Changes, plan.Change{
				Type: "file", ID: name + ".conf",
				Actions: []string{plan.ActionCreate},
				Reason:  "reverse proxy virtual host is created",
				After:   plan.StrPtr(rendered),
			})
		case rendered == vhostCurrent:
			p.Changes = append(p.Changes, plan.Change{
				Type: "file", ID: name + ".conf",
				Actions: []string{plan.ActionNoOp},
				Reason:  "virtual host is unchanged",
			})
		default:
			p.Changes = append(p.Changes, plan.Change{
				Type: "file", ID: name + ".conf",
				Actions: []string{plan.ActionUpdate},
				Reason:  "reverse proxy virtual host is regenerated",
				Before:  plan.StrPtr(vhostCurrent),
				After:   plan.StrPtr(rendered),
			})
		}
		p.Changes = append(p.Changes, s.pendingCertificateChanges(&depCopy)...)
	}

	s.finishPlan(c, p, body)
}

func (s *Server) pendingCertificateChanges(deployment *models.Deployment) []plan.Change {
	if deployment.Metadata == nil {
		return nil
	}
	var changes []plan.Change
	seen := map[string]struct{}{}
	for _, d := range deployment.Metadata.GetDomains() {
		if !d.SSL.Enabled || !d.SSL.AutoCert {
			continue
		}
		if _, dup := seen[d.Domain]; dup {
			continue
		}
		seen[d.Domain] = struct{}{}
		if !s.proxyOrchestrator.SSLManager().CertificateExists(d.Domain) {
			changes = append(changes, plan.Change{
				Type: "certificate", ID: d.Domain,
				Actions: []string{plan.ActionCreate},
				Reason:  "certificate is requested from the configured CA on apply",
			})
		}
	}
	return changes
}

func applyPlannedDomainAdd(s *Server, p *plan.Plan) (gin.H, error) {
	name := p.Resource.ID
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		return nil, apiErrf(http.StatusNotFound, "Deployment not found")
	}
	var domain models.DomainConfig
	if err := json.Unmarshal(p.Request.Body, &domain); err != nil {
		return nil, apiErrf(http.StatusBadRequest, "invalid plan body: %s", err.Error())
	}
	if err := s.mutateDomainAdd(deployment, &domain); err != nil {
		return nil, err
	}
	if err := s.manager.SaveMetadata(name, deployment.Metadata); err != nil {
		return nil, apiErrf(http.StatusInternalServerError, "Failed to save domain: %s", err.Error())
	}
	var result *proxy.SetupResult
	if s.proxyOrchestrator != nil {
		result, err = s.proxyOrchestrator.SetupDeployment(deployment)
		if err != nil {
			return nil, apiErrf(http.StatusConflict, "Failed to configure proxy: %s", err.Error())
		}
	}
	return gin.H{"message": "Domain added successfully", "domain": domain, "proxy_result": result}, nil
}

func applyPlannedDomainUpdate(s *Server, p *plan.Plan) (gin.H, error) {
	name := p.Resource.ID
	domainID := p.Request.Params["domainId"]
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		return nil, apiErrf(http.StatusNotFound, "Deployment not found")
	}
	var updatedDomain models.DomainConfig
	if err := json.Unmarshal(p.Request.Body, &updatedDomain); err != nil {
		return nil, apiErrf(http.StatusBadRequest, "invalid plan body: %s", err.Error())
	}
	if err := s.mutateDomainUpdate(deployment, domainID, &updatedDomain); err != nil {
		return nil, err
	}
	if err := s.manager.SaveMetadata(name, deployment.Metadata); err != nil {
		return nil, apiErrf(http.StatusInternalServerError, "Failed to save domain: %s", err.Error())
	}
	result, err := s.proxyOrchestrator.SetupDeployment(deployment)
	if err != nil {
		return nil, apiErrf(http.StatusConflict, "Failed to configure proxy: %s", err.Error())
	}
	return gin.H{"message": "Domain updated successfully", "domain": updatedDomain, "proxy_result": result}, nil
}

func applyPlannedDomainDelete(s *Server, p *plan.Plan) (gin.H, error) {
	name := p.Resource.ID
	domainID := p.Request.Params["domainId"]
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		return nil, apiErrf(http.StatusNotFound, "Deployment not found")
	}
	teardown, err := mutateDomainDelete(deployment, domainID)
	if err != nil {
		return nil, err
	}
	if err := s.manager.SaveMetadata(name, deployment.Metadata); err != nil {
		return nil, apiErrf(http.StatusInternalServerError, "Failed to save metadata: %s", err.Error())
	}
	if s.proxyOrchestrator != nil {
		if teardown {
			if err := s.proxyOrchestrator.TeardownDeployment(name); err != nil {
				log.Printf("Warning: failed to teardown proxy for %s: %v", name, err)
			}
		} else {
			if _, err := s.proxyOrchestrator.SetupDeployment(deployment); err != nil {
				log.Printf("Warning: failed to update proxy for %s: %v", name, err)
			}
		}
	}
	return gin.H{"message": "Domain deleted successfully"}, nil
}

func (s *Server) planProxySetup(c *gin.Context, deployment *models.Deployment) {
	name := deployment.Name

	p := s.newPlan("proxy.setup", "deployment", name)
	metaRel := filepath.Join(name, "service.yml")
	p.Snapshot.Files = plan.SnapshotFiles(s.config.DeploymentsPath, metaRel, s.vhostConfigPath(name))

	rendered, err := s.proxyOrchestrator.RenderDeployment(deployment)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	current, vhostErr := s.proxyOrchestrator.NginxManager().GetVirtualHost(name)

	switch {
	case rendered == "":
		p.Changes = append(p.Changes, plan.Change{
			Type: "file", ID: name + ".conf",
			Actions: []string{plan.ActionNoOp},
			Reason:  "deployment is not configured for exposure; nothing to set up",
		})
	case vhostErr != nil:
		p.Changes = append(p.Changes, plan.Change{
			Type: "file", ID: name + ".conf",
			Actions: []string{plan.ActionCreate},
			Reason:  "reverse proxy virtual host is created and nginx reloaded",
			After:   plan.StrPtr(rendered),
		})
	case rendered == current:
		p.Changes = append(p.Changes, plan.Change{
			Type: "file", ID: name + ".conf",
			Actions: []string{plan.ActionNoOp},
			Reason:  "virtual host already matches the desired configuration",
		})
	default:
		p.Changes = append(p.Changes, plan.Change{
			Type: "file", ID: name + ".conf",
			Actions: []string{plan.ActionUpdate},
			Reason:  "reverse proxy virtual host is regenerated and nginx reloaded",
			Before:  plan.StrPtr(current),
			After:   plan.StrPtr(rendered),
		})
	}
	if rendered != "" {
		p.Changes = append(p.Changes, s.pendingCertificateChanges(deployment)...)
	}

	s.finishPlan(c, p, nil)
}

func applyPlannedProxySetup(s *Server, p *plan.Plan) (gin.H, error) {
	deployment, err := s.manager.GetDeployment(p.Resource.ID)
	if err != nil {
		return nil, apiErrf(http.StatusNotFound, "Deployment not found")
	}
	result, err := s.proxyOrchestrator.SetupDeployment(deployment)
	if err != nil {
		return nil, err
	}
	return gin.H{"message": "Proxy setup completed", "result": result}, nil
}

func (s *Server) planConfigUpdate(c *gin.Context, key string, value interface{}) {
	entry, err := config.Get(s.config, key)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	p := s.newPlan("config.update", "config", "_global")
	if s.configPath != "" {
		// Absolute so snapshot verification never resolves it against
		// the deployments dir.
		if absConfig, aerr := filepath.Abs(s.configPath); aerr == nil {
			p.Snapshot.Files = plan.SnapshotFiles(s.config.DeploymentsPath, absConfig)
		}
	}

	beforeJSON, _ := json.Marshal(entry.Value)
	afterJSON, _ := json.Marshal(value)

	_, hasApplier := s.runtimeAppliers()[key]
	effect := "saved to the config file; takes effect after the agent restarts"
	if hasApplier {
		effect = "saved to the config file and applied to the running agent immediately"
	}

	if string(beforeJSON) == string(afterJSON) {
		p.Changes = append(p.Changes, plan.Change{
			Type: "config", ID: key,
			Actions: []string{plan.ActionNoOp},
			Reason:  "value is unchanged",
		})
	} else {
		p.Changes = append(p.Changes, plan.Change{
			Type: "config", ID: key,
			Actions:   []string{plan.ActionUpdate},
			Reason:    effect,
			Before:    plan.StrPtr(string(beforeJSON)),
			After:     plan.StrPtr(string(afterJSON)),
			Sensitive: entry.Sensitive,
		})
	}

	s.finishPlan(c, p, gin.H{"value": value})
}

func applyPlannedConfigUpdate(s *Server, p *plan.Plan) (gin.H, error) {
	key := normalizeConfigKey(p.Request.Params["key"])
	if key == "" {
		return nil, apiErrf(http.StatusBadRequest, "plan is missing the config key")
	}
	var req struct {
		Value interface{} `json:"value"`
	}
	if err := json.Unmarshal(p.Request.Body, &req); err != nil {
		return nil, apiErrf(http.StatusBadRequest, "invalid plan body: %s", err.Error())
	}
	outcome, err := s.applyConfigUpdate(key, req.Value)
	if err != nil {
		return nil, err
	}
	resp := gin.H{"entry": outcome.Entry, "applied": outcome.Applied}
	if outcome.ApplyErr != nil {
		resp["apply_error"] = outcome.ApplyErr.Error()
	}
	return resp, nil
}
