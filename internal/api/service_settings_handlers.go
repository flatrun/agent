package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"

	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/plan"
	"github.com/flatrun/agent/pkg/models"
)

func (s *Server) getServiceSettings(c *gin.Context) {
	name, service := c.Param("name"), c.Param("service")
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}
	if _, err := s.resolveService(name, service); err != nil || service == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "Service not found"})
		return
	}
	settings := models.ServiceSettings{}
	if deployment.Metadata != nil {
		settings = deployment.Metadata.ServiceSettings[service]
	}
	c.JSON(http.StatusOK, gin.H{"service": service, "settings": settings})
}

// updateServiceSettings replaces a service's compose settings. They are
// saved to service.yml and rendered into the compose overlay; the running
// container keeps its old settings until the service is recreated.
func (s *Server) updateServiceSettings(c *gin.Context) {
	name, service := c.Param("name"), c.Param("service")
	var req models.ServiceSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
		return
	}

	if planRequested(c) {
		s.planServiceSettings(c, name, service, &req)
		return
	}
	if !s.requirePlannedAction(c, name) {
		return
	}
	if !s.requireUnprotectedDeploymentAction(c, name, protectedActionUpdateDeployment) {
		return
	}
	if err := s.commitServiceSettings(name, service, &req); err != nil {
		respondAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"service": service, "settings": req, "applied": false})
}

// nextServiceSettings validates req and returns the deployment's metadata
// with it in place, leaving the stored metadata untouched.
func (s *Server) nextServiceSettings(name, service string, req *models.ServiceSettings) (*models.Deployment, *models.ServiceMetadata, error) {
	normalizeServiceSettings(req)
	if err := req.Validate(); err != nil {
		return nil, nil, apiErrf(http.StatusBadRequest, "%s", capitalize(err.Error()))
	}
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		return nil, nil, apiErrf(http.StatusNotFound, "Deployment not found")
	}
	if service == "" {
		return nil, nil, apiErrf(http.StatusNotFound, "Service not found")
	}
	if _, err := s.resolveService(name, service); err != nil && !req.IsEmpty() {
		return nil, nil, apiErrf(http.StatusNotFound, "Service %s is not in the compose file", service)
	}
	next, err := cloneMetadata(deployment.Metadata)
	if err != nil {
		return nil, nil, err
	}
	if next == nil {
		next = &models.ServiceMetadata{Name: name}
	}
	if next.ServiceSettings == nil {
		next.ServiceSettings = map[string]models.ServiceSettings{}
	}
	if req.IsEmpty() {
		delete(next.ServiceSettings, service)
	} else {
		next.ServiceSettings[service] = *req
	}
	if len(next.ServiceSettings) == 0 {
		next.ServiceSettings = nil
	}
	return deployment, next, nil
}

func (s *Server) commitServiceSettings(name, service string, req *models.ServiceSettings) error {
	deployment, next, err := s.nextServiceSettings(name, service, req)
	if err != nil {
		return err
	}
	if err := s.manager.ApplyServiceSettings(name, next.ServiceSettings); err != nil {
		var invalid *docker.OverlayValidationError
		if errors.As(err, &invalid) {
			return apiErrf(http.StatusUnprocessableEntity, "%s", invalid.Error())
		}
		return apiErrf(http.StatusInternalServerError, "Failed to write the compose overlay: %s", err.Error())
	}
	if err := s.manager.SaveMetadata(name, next); err != nil {
		var previous map[string]models.ServiceSettings
		if deployment.Metadata != nil {
			previous = deployment.Metadata.ServiceSettings
		}
		_ = s.manager.ApplyServiceSettings(name, previous)
		return apiErrf(http.StatusInternalServerError, "Failed to save the service settings: %s", err.Error())
	}
	return nil
}

func (s *Server) planServiceSettings(c *gin.Context, name, service string, req *models.ServiceSettings) {
	deployment, next, err := s.nextServiceSettings(name, service, req)
	if err != nil {
		respondAPIError(c, err)
		return
	}
	metaRel := filepath.Join(name, "service.yml")
	overlayRel := filepath.Join(name, models.ComposeOverlayFile)

	sensitive := hasServiceEnvironment(next.ServiceSettings)
	if deployment.Metadata != nil {
		sensitive = sensitive || hasServiceEnvironment(deployment.Metadata.ServiceSettings)
	}

	p := s.newPlan("deployment.service.settings", "deployment", name)
	p.Snapshot.Files = plan.SnapshotFiles(s.config.DeploymentsPath, metaRel, overlayRel)

	beforeMeta, _ := yaml.Marshal(deployment.Metadata)
	afterMeta, err := yaml.Marshal(next)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	p.Changes = append(p.Changes, plan.Change{
		Type: "file", ID: "service.yml",
		Actions: []string{plan.ActionUpdate},
		Reason:  "the settings of " + service + " are saved with the deployment",
		Before:  plan.StrPtr(string(beforeMeta)), After: plan.StrPtr(string(afterMeta)),
		Sensitive: sensitive,
	})

	current, _ := s.manager.ComposeOverlay(name)
	rendered, err := docker.RenderOverlay(next.ServiceSettings)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	overlay := plan.Change{Type: "file", ID: models.ComposeOverlayFile, Sensitive: sensitive}
	switch {
	case current == string(rendered):
		overlay.Actions = []string{plan.ActionNoOp}
		overlay.Reason = "the compose overlay is unchanged"
	case current == "":
		overlay.Actions = []string{plan.ActionCreate}
		overlay.Reason = "the compose overlay is created and passed to compose after the deployment's own file"
		overlay.After = plan.StrPtr(string(rendered))
	case rendered == nil:
		overlay.Actions = []string{plan.ActionDelete}
		overlay.Reason = "no service has settings left, so the compose overlay is removed"
		overlay.Before = plan.StrPtr(current)
	default:
		overlay.Actions = []string{plan.ActionUpdate}
		overlay.Reason = "the compose overlay is regenerated"
		overlay.Before, overlay.After = plan.StrPtr(current), plan.StrPtr(string(rendered))
	}
	p.Changes = append(p.Changes, overlay)

	s.finishPlan(c, p, req)
}

func applyPlannedServiceSettings(s *Server, p *plan.Plan) (gin.H, error) {
	service := p.Request.Params["service"]
	var req models.ServiceSettings
	if err := json.Unmarshal(p.Request.Body, &req); err != nil {
		return nil, apiErrf(http.StatusBadRequest, "invalid plan body: %s", err.Error())
	}
	if err := s.commitServiceSettings(p.Resource.ID, service, &req); err != nil {
		return nil, err
	}
	return gin.H{"service": service, "settings": req, "applied": false}, nil
}

func normalizeServiceSettings(req *models.ServiceSettings) {
	if r := req.Resources; r != nil {
		r.CPUs = strings.TrimSpace(r.CPUs)
		r.Memory = strings.TrimSpace(r.Memory)
		if r.CPUs == "" && r.Memory == "" {
			req.Resources = nil
		}
	}
	req.Restart = strings.TrimSpace(req.Restart)
	if l := req.Logging; l != nil {
		l.MaxSize = strings.TrimSpace(l.MaxSize)
		if l.MaxSize == "" && l.MaxFiles == 0 {
			req.Logging = nil
		}
	}
	if len(req.Environment) == 0 {
		req.Environment = nil
	}
	if len(req.Command) == 0 {
		req.Command = nil
	}
	if len(req.Entrypoint) == 0 {
		req.Entrypoint = nil
	}
}

func capitalize(msg string) string {
	if msg == "" {
		return msg
	}
	return strings.ToUpper(msg[:1]) + msg[1:]
}

func hasServiceEnvironment(settings map[string]models.ServiceSettings) bool {
	for _, svc := range settings {
		if len(svc.Environment) > 0 {
			return true
		}
	}
	return false
}
