package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/build"
	"github.com/flatrun/agent/internal/deploys"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/workspace"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

var environmentNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,29}$`)

var errEnvironmentExists = errors.New("environment already exists")

type environmentRequest struct {
	Name   string `json:"name" binding:"required"`
	Branch string `json:"branch"`
	// Revision pins the first deploy to a commit, which also reaches pull
	// requests from forks whose branch does not exist in the base repository.
	Revision string `json:"-"`
	// PR marks the environment as a pull request preview.
	PR      int             `json:"-"`
	Trigger deploys.Trigger `json:"-"`
}

func environmentDeploymentName(base, env string) string {
	return base + "-" + env
}

// createEnvironment makes a sibling of base that follows its own branch. It
// copies the base's variables and network attachments, records the fetched
// revision, and starts a redeploy job that builds (when needed) and runs it.
func (s *Server) createEnvironment(ctx context.Context, base string, req environmentRequest) (string, *ActionJob, error) {
	if !environmentNamePattern.MatchString(req.Name) {
		return "", nil, fmt.Errorf("environment name must be lowercase letters, digits and dashes")
	}
	baseDeployment, err := s.manager.GetDeployment(base)
	if err != nil {
		return "", nil, err
	}
	baseMeta := baseDeployment.Metadata
	if baseMeta == nil || baseMeta.Source == nil {
		return "", nil, errNoSource
	}
	if baseMeta.Environment != nil {
		return "", nil, fmt.Errorf("%s is itself an environment of %s", base, baseMeta.Environment.Base)
	}

	name := environmentDeploymentName(base, req.Name)
	if _, err := s.manager.GetDeployment(name); err == nil {
		return name, nil, errEnvironmentExists
	}
	envVars, err := s.readPrincipalEnv(base)
	if err != nil {
		return "", nil, fmt.Errorf("read variables of %s: %w", base, err)
	}

	src := deploymentSourceFrom(baseMeta.Source)
	if req.Branch != "" {
		src.Branch = req.Branch
	}
	src.Revision = req.Revision
	fetched, err := s.fetchDeploymentSource(ctx, src)
	if err != nil {
		return "", nil, err
	}
	defer fetched.cleanup()

	baseCompose, _, _ := s.manager.GetComposeFile(base)
	var compose string
	if fetched.builder == build.KindCompose {
		compose = fetched.composeContent
	} else if compose, err = s.sourceImageCompose(name, build.ImageTag(name, fetched.revision), baseMeta.Networking.ContainerPort); err != nil {
		return "", nil, err
	}
	if named, err := docker.EnsureContainerNames(compose, name); err == nil {
		compose = named
	}
	if baseCompose != "" {
		if carried, err := docker.CarryExternalNetworks(baseCompose, compose); err == nil {
			compose = carried
		}
	}

	if err := s.validateNewComposeContent(compose, name, envVars, fetched.dir); err != nil {
		return "", nil, fmt.Errorf("invalid compose content in source: %w", err)
	}
	if err := s.manager.CreateDeploymentFromSource(name, fetched.dir, compose, fetched.composeName); err != nil {
		return "", nil, err
	}
	discard := func(err error) (string, *ActionJob, error) {
		if delErr := s.manager.DeleteDeployment(name); delErr != nil {
			log.Printf("Warning: failed to remove incomplete environment %s: %v", name, delErr)
		}
		return "", nil, err
	}
	if len(envVars) > 0 || fetched.builder != build.KindCompose {
		if err := s.writeEnvFile(name, envVars); err != nil {
			return discard(err)
		}
	}

	meta := &models.ServiceMetadata{
		Name:           name,
		Type:           baseMeta.Type,
		Kind:           baseMeta.Kind,
		PrimaryService: baseMeta.PrimaryService,
		Networking:     baseMeta.Networking,
		HealthCheck:    baseMeta.HealthCheck,
		Security:       baseMeta.Security,
		CredentialID:   baseMeta.CredentialID,
		Source: &models.SourceConfig{
			Type:         src.Type,
			Ref:          src.Ref,
			Branch:       src.Branch,
			Subpath:      src.Subpath,
			CredentialID: src.CredentialID,
			AutoDeploy:   baseMeta.Source.AutoDeploy,
			Builder:      string(fetched.builder),
			LastRevision: fetched.revision,
		},
		Environment: &models.EnvironmentInfo{Base: base, Name: req.Name, PR: req.PR},
	}
	meta.Networking.Domain = ""
	if meta.Networking.Expose && s.config.Domain.DefaultDomain != "" {
		meta.Networking.Domain = name + "." + s.config.Domain.DefaultDomain
	} else {
		meta.Networking.Expose = false
	}
	if err := s.manager.SaveMetadata(name, meta); err != nil {
		return discard(err)
	}
	if store := s.workspaces(); store != nil {
		if owner, err := store.Owner(workspace.Ref{Type: workspace.TypeDeployment, ID: base}); err == nil {
			s.assignDeploymentWorkspace(owner, name)
		}
	}

	if meta.Networking.Expose && s.proxyOrchestrator != nil {
		if deployment, err := s.manager.GetDeployment(name); err == nil {
			if _, err := s.setupProxyWithRetry(deployment, 3); err != nil {
				log.Printf("Warning: failed to set up proxy for environment %s: %v", name, err)
			}
		}
	}

	trigger := req.Trigger
	if trigger == "" {
		trigger = deploys.TriggerManual
	}
	job, _ := s.startDeploymentJob(name, "redeploy", actionOptions{Revision: fetched.revision, Trigger: string(trigger)})
	return name, job, nil
}

// environmentsOf lists the deployments that are environments of base.
func (s *Server) environmentsOf(base string) ([]models.Deployment, error) {
	all, err := s.manager.ListDeployments()
	if err != nil {
		return nil, err
	}
	var envs []models.Deployment
	for _, d := range all {
		if d.Metadata != nil && d.Metadata.Environment != nil && d.Metadata.Environment.Base == base {
			envs = append(envs, d)
		}
	}
	return envs, nil
}

func (s *Server) listEnvironments(c *gin.Context) {
	envs, err := s.environmentsOf(c.Param("name"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if envs == nil {
		envs = []models.Deployment{}
	}
	c.JSON(http.StatusOK, gin.H{"environments": envs})
}

func (s *Server) createEnvironmentHandler(c *gin.Context) {
	var req environmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name, job, err := s.createEnvironment(c.Request.Context(), c.Param("name"), req)
	switch {
	case errors.Is(err, errEnvironmentExists):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "name": name})
		return
	case err != nil:
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if s.authManager != nil {
		actor := auth.GetActorFromContext(c)
		if actor != nil && actor.User != nil && actor.Role != auth.RoleAdmin {
			_ = s.authManager.AssignDeployment(actor.User.ID, name, auth.AccessLevelAdmin, actor.User.ID)
		}
	}

	resp := gin.H{"name": name}
	if job != nil {
		resp["job_id"] = job.ID()
	}
	c.JSON(http.StatusCreated, resp)
}
