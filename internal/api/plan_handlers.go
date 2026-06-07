package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/plan"
	"github.com/gin-gonic/gin"
)

type planAction struct {
	Permission      auth.Permission
	AccessLevel     string
	ProtectedAction string
	SuccessStatus   int
	Apply           func(s *Server, p *plan.Plan) (gin.H, error)
}

func (s *Server) planRegistry() map[string]planAction {
	return map[string]planAction{
		"deployment.env.update": {
			Permission:      auth.PermDeploymentsWrite,
			AccessLevel:     auth.AccessLevelWrite,
			ProtectedAction: protectedActionUpdateEnv,
			Apply:           applyPlannedEnvUpdate,
		},
		"deployment.compose.update": {
			Permission:      auth.PermDeploymentsWrite,
			AccessLevel:     auth.AccessLevelWrite,
			ProtectedAction: protectedActionUpdateDeployment,
			Apply:           applyPlannedComposeUpdate,
		},
		"deployment.delete": {
			Permission:      auth.PermDeploymentsDelete,
			AccessLevel:     auth.AccessLevelAdmin,
			ProtectedAction: protectedActionDeleteDeployment,
			Apply:           applyPlannedDeploymentDelete,
		},
		"deployment.domain.add": {
			Permission:    auth.PermDeploymentsWrite,
			AccessLevel:   auth.AccessLevelWrite,
			SuccessStatus: http.StatusCreated,
			Apply:         applyPlannedDomainAdd,
		},
		"deployment.domain.update": {
			Permission:  auth.PermDeploymentsWrite,
			AccessLevel: auth.AccessLevelWrite,
			Apply:       applyPlannedDomainUpdate,
		},
		"deployment.domain.delete": {
			Permission:  auth.PermDeploymentsWrite,
			AccessLevel: auth.AccessLevelWrite,
			Apply:       applyPlannedDomainDelete,
		},
		"proxy.setup": {
			Permission:  auth.PermCertificatesWrite,
			AccessLevel: auth.AccessLevelWrite,
			Apply:       applyPlannedProxySetup,
		},
		"config.update": {
			Permission: auth.PermConfigWrite,
			Apply:      applyPlannedConfigUpdate,
		},
		"deployment.service.start": {
			Permission:  auth.PermDeploymentsWrite,
			AccessLevel: auth.AccessLevelWrite,
			Apply:       applyPlannedServiceAction("start"),
		},
		"deployment.service.stop": {
			Permission:  auth.PermDeploymentsWrite,
			AccessLevel: auth.AccessLevelWrite,
			Apply:       applyPlannedServiceAction("stop"),
		},
		"deployment.service.restart": {
			Permission:  auth.PermDeploymentsWrite,
			AccessLevel: auth.AccessLevelWrite,
			Apply:       applyPlannedServiceAction("restart"),
		},
		"deployment.service.pull": {
			Permission:  auth.PermDeploymentsWrite,
			AccessLevel: auth.AccessLevelWrite,
			Apply:       applyPlannedServiceAction("pull"),
		},
		"deployment.service.rebuild": {
			Permission:      auth.PermDeploymentsWrite,
			AccessLevel:     auth.AccessLevelWrite,
			ProtectedAction: protectedActionRebuild,
			Apply:           applyPlannedServiceAction("rebuild"),
		},
	}
}

func planRequested(c *gin.Context) bool {
	return c.Query("plan") == "true"
}

// requirePlannedAction enforces the deployment's require_plan setting:
// when set, direct execution is refused and the caller must create and
// apply a plan instead. Plan creation itself is always allowed, and
// applies bypass this guard because they run the reviewed plan.
func (s *Server) requirePlannedAction(c *gin.Context, name string) bool {
	if planRequested(c) {
		return true
	}
	deployment, err := s.manager.GetDeployment(name)
	if err != nil || deployment.Metadata == nil || !deployment.Metadata.RequirePlan {
		return true
	}
	c.JSON(http.StatusPreconditionRequired, gin.H{
		"error": "This deployment requires changes to be planned and reviewed before they run",
		"code":  "plan_required",
	})
	return false
}

func planActorFrom(c *gin.Context) plan.Actor {
	actor := auth.GetActorFromContext(c)
	if actor == nil {
		return plan.Actor{Type: "anonymous"}
	}
	a := plan.Actor{Type: actor.Type}
	switch {
	case actor.User != nil:
		a.ID = fmt.Sprintf("%d", actor.User.ID)
		a.Name = actor.User.Username
	case actor.APIKey != nil:
		a.ID = actor.APIKey.KeyID
		a.Name = actor.APIKey.Name
	}
	return a
}

func (s *Server) newPlan(action, resourceType, resourceID string) *plan.Plan {
	return plan.New(action, plan.Resource{Type: resourceType, ID: resourceID}, plan.Actor{}, s.config.Plans.TTL)
}

// finishPlan stamps actor and request context onto the plan, persists
// it, and answers the original mutating request with the preview
// instead of executing it.
func (s *Server) finishPlan(c *gin.Context, p *plan.Plan, body interface{}) {
	p.CreatedBy = planActorFrom(c)
	p.Request.Method = c.Request.Method
	p.Request.Path = c.Request.URL.Path

	params := map[string]string{}
	for _, prm := range c.Params {
		params[prm.Key] = prm.Value
	}
	if len(params) > 0 {
		p.Request.Params = params
	}

	query := map[string]string{}
	for k, v := range c.Request.URL.Query() {
		if k == "plan" || len(v) == 0 {
			continue
		}
		query[k] = v[0]
	}
	if len(query) > 0 {
		p.Request.Query = query
	}

	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to encode plan request: " + err.Error()})
			return
		}
		p.Request.Body = raw
	}

	p.Summarize()

	if err := s.planStore.Save(p); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save plan: " + err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"plan": p.Redacted()})
}

func (s *Server) canViewPlan(c *gin.Context, p *plan.Plan) bool {
	actor := auth.GetActorFromContext(c)
	if actor == nil {
		return true
	}
	if p.Resource.Type == "deployment" {
		return actor.CanAccessDeployment(p.Resource.ID, auth.AccessLevelRead)
	}
	return actor.HasPermission(auth.PermConfigRead)
}

func (s *Server) canManagePlan(c *gin.Context, p *plan.Plan) (planAction, *apiError) {
	action, ok := s.planRegistry()[p.Action]
	if !ok {
		return planAction{}, apiErrf(http.StatusConflict, "plan action %q is not supported by this agent", p.Action)
	}
	actor := auth.GetActorFromContext(c)
	if actor != nil {
		if !actor.HasPermission(action.Permission) {
			return planAction{}, apiErrf(http.StatusForbidden, "Permission denied: %s required", action.Permission)
		}
		if action.AccessLevel != "" && p.Resource.Type == "deployment" && !actor.CanAccessDeployment(p.Resource.ID, action.AccessLevel) {
			return planAction{}, apiErrf(http.StatusForbidden, "No access to this deployment")
		}
	}
	return action, nil
}

// reverifyPlan lazily transitions an available plan to expired or
// obsolete, so reads always reflect reality even when the change that
// invalidated the plan happened outside the API (e.g. an SSH edit).
func (s *Server) reverifyPlan(p *plan.Plan) *plan.Plan {
	if p.Status != plan.StatusAvailable {
		return p
	}
	if p.Expired(time.Now().UTC()) {
		p.Status = plan.StatusExpired
		_ = s.planStore.Save(p)
		return p
	}
	if err := plan.VerifySnapshot(s.config.DeploymentsPath, p.Snapshot.Files); err != nil {
		p.Status = plan.StatusObsolete
		_ = s.planStore.Save(p)
	}
	return p
}

func (s *Server) listPlans(c *gin.Context) {
	filter := plan.ListFilter{
		ResourceType: c.Query("resource_type"),
		ResourceID:   c.Query("deployment"),
	}
	if filter.ResourceID != "" && filter.ResourceType == "" {
		filter.ResourceType = "deployment"
	}

	plans, err := s.planStore.List(filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	statusFilter := c.Query("status")
	out := make([]*plan.Plan, 0, len(plans))
	for _, p := range plans {
		if !s.canViewPlan(c, p) {
			continue
		}
		p = s.reverifyPlan(p)
		if statusFilter != "" && p.Status != statusFilter {
			continue
		}
		out = append(out, p.Redacted())
	}
	c.JSON(http.StatusOK, gin.H{"plans": out})
}

func (s *Server) getPlan(c *gin.Context) {
	p, err := s.planStore.Get(c.Param("id"))
	if err == plan.ErrNotFound {
		c.JSON(http.StatusNotFound, gin.H{"error": "Plan not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !s.canViewPlan(c, p) {
		c.JSON(http.StatusForbidden, gin.H{"error": "No access to this plan"})
		return
	}
	p = s.reverifyPlan(p)

	if c.Query("include_sensitive") == "true" {
		if _, aerr := s.canManagePlan(c, p); aerr != nil {
			respondAPIError(c, aerr)
			return
		}
		c.JSON(http.StatusOK, gin.H{"plan": p})
		return
	}
	c.JSON(http.StatusOK, gin.H{"plan": p.Redacted()})
}

func (s *Server) applyPlan(c *gin.Context) {
	id := c.Param("id")
	p, err := s.planStore.Get(id)
	if err == plan.ErrNotFound {
		c.JSON(http.StatusNotFound, gin.H{"error": "Plan not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	unlock := s.planStore.LockResource(p.Resource)
	defer unlock()

	// Re-read under the resource lock: a concurrent apply may have
	// just transitioned this plan.
	p, err = s.planStore.Get(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Plan not found"})
		return
	}

	action, aerr := s.canManagePlan(c, p)
	if aerr != nil {
		respondAPIError(c, aerr)
		return
	}

	if p.Status == plan.StatusAvailable && p.Expired(time.Now().UTC()) {
		p.Status = plan.StatusExpired
		_ = s.planStore.Save(p)
	}
	switch p.Status {
	case plan.StatusAvailable:
	case plan.StatusExpired:
		c.JSON(http.StatusGone, gin.H{"error": "Plan has expired; create a new plan", "plan": p.Redacted()})
		return
	default:
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("Plan is %s and can no longer be applied", p.Status), "plan": p.Redacted()})
		return
	}

	if action.ProtectedAction != "" && p.Resource.Type == "deployment" {
		blocked, reason, perr := s.protectedDeploymentActionBlocked(p.Resource.ID, action.ProtectedAction)
		if perr == nil && blocked {
			c.JSON(http.StatusLocked, gin.H{"error": reason})
			return
		}
	}

	if verr := plan.VerifySnapshot(s.config.DeploymentsPath, p.Snapshot.Files); verr != nil {
		p.Status = plan.StatusObsolete
		_ = s.planStore.Save(p)
		resp := gin.H{"error": "Plan is stale: state changed since it was created", "plan": p.Redacted()}
		if drift, ok := verr.(*plan.DriftError); ok {
			resp["drifted"] = drift.Paths
		}
		c.JSON(http.StatusConflict, resp)
		return
	}

	p.Status = plan.StatusApplying
	if err := s.planStore.Save(p); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to persist plan state: " + err.Error()})
		return
	}

	result, applyErr := action.Apply(s, p)

	now := time.Now().UTC()
	actor := planActorFrom(c)
	p.AppliedAt = &now
	p.AppliedBy = &actor

	if applyErr != nil {
		p.Status = plan.StatusFailed
		p.ApplyError = applyErr.Error()
		_ = s.planStore.Save(p)
		status := http.StatusInternalServerError
		if ae, ok := applyErr.(*apiError); ok {
			status = ae.Status
		}
		c.JSON(status, gin.H{"error": applyErr.Error(), "plan": p.Redacted()})
		return
	}

	p.Status = plan.StatusApplied
	_ = s.planStore.Save(p)

	// Other still-open plans on this resource were computed against
	// state that no longer exists.
	siblings, _ := s.planStore.List(plan.ListFilter{
		ResourceType: p.Resource.Type,
		ResourceID:   p.Resource.ID,
		Status:       plan.StatusAvailable,
	})
	for _, sib := range siblings {
		if sib.ID == p.ID {
			continue
		}
		sib.Status = plan.StatusObsolete
		_ = s.planStore.Save(sib)
	}

	resp := gin.H{"plan": p.Redacted()}
	for k, v := range result {
		resp[k] = v
	}
	status := action.SuccessStatus
	if status == 0 {
		status = http.StatusOK
	}
	c.JSON(status, resp)
}

func (s *Server) deletePlan(c *gin.Context) {
	p, err := s.planStore.Get(c.Param("id"))
	if err == plan.ErrNotFound {
		c.JSON(http.StatusNotFound, gin.H{"error": "Plan not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if _, aerr := s.canManagePlan(c, p); aerr != nil {
		respondAPIError(c, aerr)
		return
	}
	if err := s.planStore.Delete(p.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Plan discarded", "id": p.ID})
}
