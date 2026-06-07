package api

import (
	"net/http"
	"path/filepath"

	"github.com/flatrun/agent/internal/plan"
	"github.com/gin-gonic/gin"
)

type serviceActionSpec struct {
	verb            string
	planAction      string
	protectedAction string
	message         string
	changeActions   []string
	changeReason    string
	run             func(s *Server, name, service string) (string, error)
}

func serviceActionSpecs() map[string]serviceActionSpec {
	return map[string]serviceActionSpec{
		"start": {
			verb:          "start",
			planAction:    "deployment.service.start",
			message:       "Service started",
			changeActions: []string{plan.ActionCreate},
			changeReason:  "containers for this service are started, created from the current compose file if missing",
			run: func(s *Server, name, service string) (string, error) {
				auth, opts := s.deploymentAuthOptions(name)
				defer auth.Close()
				return s.manager.StartService(name, service, opts...)
			},
		},
		"stop": {
			verb:          "stop",
			planAction:    "deployment.service.stop",
			message:       "Service stopped",
			changeActions: []string{plan.ActionDelete},
			changeReason:  "containers for this service are stopped; data and configuration are untouched",
			run: func(s *Server, name, service string) (string, error) {
				return s.manager.StopService(name, service)
			},
		},
		"restart": {
			verb:          "restart",
			planAction:    "deployment.service.restart",
			message:       "Service restarted",
			changeActions: []string{plan.ActionDelete, plan.ActionCreate},
			changeReason:  "containers for this service are stopped and started again",
			run: func(s *Server, name, service string) (string, error) {
				auth, opts := s.deploymentAuthOptions(name)
				defer auth.Close()
				return s.manager.RestartService(name, service, opts...)
			},
		},
		"rebuild": {
			verb:            "rebuild",
			planAction:      "deployment.service.rebuild",
			protectedAction: protectedActionRebuild,
			message:         "Service rebuilt",
			changeActions:   []string{plan.ActionDelete, plan.ActionCreate},
			changeReason:    "service image is rebuilt and its containers are recreated from the current compose file",
			run: func(s *Server, name, service string) (string, error) {
				auth, opts := s.deploymentAuthOptions(name)
				defer auth.Close()
				return s.manager.RebuildService(name, service, opts...)
			},
		},
	}
}

func (s *Server) serviceActionHandler(verb string) gin.HandlerFunc {
	spec := serviceActionSpecs()[verb]
	return func(c *gin.Context) {
		name := c.Param("name")
		if spec.protectedAction != "" && !s.requireUnprotectedDeploymentAction(c, name, spec.protectedAction) {
			return
		}

		service, err := s.resolveService(name, c.Param("service"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if planRequested(c) {
			s.planServiceAction(c, name, service, spec)
			return
		}
		if !s.requirePlannedAction(c, name) {
			return
		}

		output, err := spec.run(s, name, service)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "output": output})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"message": spec.message,
			"name":    name,
			"service": service,
			"output":  output,
		})
	}
}

func (s *Server) planServiceAction(c *gin.Context, name, service string, spec serviceActionSpec) {
	p := s.newPlan(spec.planAction, "deployment", name)
	if _, composeName, err := s.manager.GetComposeFile(name); err == nil {
		p.Snapshot.Files = plan.SnapshotFiles(s.config.DeploymentsPath, filepath.Join(name, composeName))
	}
	p.Changes = append(p.Changes, plan.Change{
		Type: "service", ID: service,
		Actions: spec.changeActions,
		Reason:  spec.changeReason,
	})
	s.finishPlan(c, p, nil)
}

func applyPlannedServiceAction(verb string) func(*Server, *plan.Plan) (gin.H, error) {
	return func(s *Server, p *plan.Plan) (gin.H, error) {
		spec := serviceActionSpecs()[verb]
		service := p.Request.Params["service"]
		if service == "" {
			return nil, apiErrf(http.StatusBadRequest, "plan is missing the service name")
		}
		resolved, err := s.resolveService(p.Resource.ID, service)
		if err != nil {
			return nil, apiErrf(http.StatusBadRequest, "%s", err.Error())
		}
		output, err := spec.run(s, p.Resource.ID, resolved)
		if err != nil {
			return nil, err
		}
		return gin.H{
			"message": spec.message,
			"name":    p.Resource.ID,
			"service": resolved,
			"output":  output,
		}, nil
	}
}
