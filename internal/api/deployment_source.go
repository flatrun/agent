package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/flatrun/agent/internal/build"
	"github.com/flatrun/agent/internal/deploys"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/source"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

// deploymentSource is the create-deployment request field that points at code to
// deploy from, instead of inline compose content. Type selects the provider
// (e.g. "git"); Ref is the provider's locator (a repository URL for git).
type deploymentSource struct {
	Type    string `json:"type"`
	Ref     string `json:"ref"`
	Branch  string `json:"branch,omitempty"`
	Subpath string `json:"subpath,omitempty"`
	// CredentialID names a stored credential to authenticate a private source.
	// Username and Token authenticate inline instead, for a one-off private
	// source with no saved credential. CredentialID wins when both are given.
	CredentialID string `json:"credential_id,omitempty"`
	Username     string `json:"username,omitempty"`
	Token        string `json:"token,omitempty"`
	// Revision pins the fetch to one commit instead of the branch head.
	Revision   string `json:"revision,omitempty"`
	AutoDeploy bool   `json:"auto_deploy,omitempty"`
	Previews   bool   `json:"previews,omitempty"`
}

// sourceConfig is what gets persisted on the deployment. Inline credentials are
// never stored, so a source fetched with a one-off token cannot be refetched.
func (d *deploymentSource) sourceConfig(revision string) *models.SourceConfig {
	return &models.SourceConfig{
		Type:         d.Type,
		Ref:          d.Ref,
		Branch:       d.Branch,
		Subpath:      d.Subpath,
		CredentialID: d.CredentialID,
		AutoDeploy:   d.AutoDeploy,
		Previews:     d.Previews,
		LastRevision: revision,
	}
}

func deploymentSourceFrom(cfg *models.SourceConfig) *deploymentSource {
	return &deploymentSource{
		Type:         cfg.Type,
		Ref:          cfg.Ref,
		Branch:       cfg.Branch,
		Subpath:      cfg.Subpath,
		CredentialID: cfg.CredentialID,
		AutoDeploy:   cfg.AutoDeploy,
		Previews:     cfg.Previews,
	}
}

// fetchedSource is a materialized source ready to become a deployment: the
// directory holding its files, the compose file found inside it, and the compose
// content read from it. cleanup removes the temporary fetch directory.
type fetchedSource struct {
	dir            string
	composeName    string
	composeContent string
	// builder is how the source runs: its own compose file, or an image built
	// from it, in which case composeContent is empty until the agent writes one.
	builder  build.Kind
	revision string
	message  string
	cleanup  func()
}

// fetchDeploymentSource resolves the source's provider, fetches into a temporary
// directory, and locates the compose file the deployment will run. The temporary
// directory is the caller's to release through the returned cleanup once the
// files have been copied into the deployment.
func (s *Server) fetchDeploymentSource(ctx context.Context, req *deploymentSource) (*fetchedSource, error) {
	return s.fetchDeploymentSourceWithLog(ctx, req, nil)
}

// fetchDeploymentSourceWithLog is fetchDeploymentSource with fetch progress sent
// to logf, so a redeploy job can stream it. A nil logf writes to the agent log.
func (s *Server) fetchDeploymentSourceWithLog(ctx context.Context, req *deploymentSource, logf func(string)) (*fetchedSource, error) {
	if logf == nil {
		logf = func(line string) { log.Printf("[source:%s] %s", req.Type, line) }
	}
	provider, ok := s.sourceRegistry.Get(req.Type)
	if !ok {
		return nil, fmt.Errorf("unknown source type %q", req.Type)
	}

	auth, err := s.resolveSourceAuth(req)
	if err != nil {
		return nil, err
	}

	tmp, err := os.MkdirTemp("", "flatrun-source-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create fetch directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }

	descriptor := source.Descriptor{
		Type: req.Type,
		Ref:  req.Ref,
		Params: map[string]string{
			"branch":   req.Branch,
			"subpath":  req.Subpath,
			"revision": req.Revision,
		},
		Auth: auth,
	}

	result, err := provider.Fetch(ctx, descriptor, filepath.Join(tmp, "src"), logf)
	if err != nil {
		cleanup()
		return nil, err
	}

	kind := s.builders.Detect(result.Dir)
	if kind == "" {
		cleanup()
		return nil, fmt.Errorf("no compose file found and no builder can build this source")
	}
	if kind != build.KindCompose {
		// The compose file the agent writes for a built image loads this file,
		// so validating it against the fetched tree needs it present.
		envPath := filepath.Join(result.Dir, ".env.flatrun")
		if _, err := os.Stat(envPath); os.IsNotExist(err) {
			if err := os.WriteFile(envPath, nil, 0600); err != nil {
				cleanup()
				return nil, err
			}
		}
		return &fetchedSource{
			dir:         result.Dir,
			composeName: "docker-compose.yml",
			builder:     kind,
			revision:    result.Revision,
			message:     result.Message,
			cleanup:     cleanup,
		}, nil
	}

	composePath := docker.FindComposeFile(result.Dir)

	content, err := os.ReadFile(composePath)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to read compose file from source: %w", err)
	}

	return &fetchedSource{
		dir:            result.Dir,
		composeName:    filepath.Base(composePath),
		composeContent: string(content),
		builder:        build.KindCompose,
		revision:       result.Revision,
		message:        result.Message,
		cleanup:        cleanup,
	}, nil
}

// resolveSourceAuth turns a request's credential into provider auth: a stored
// credential id when given, otherwise an inline token. Neither means an
// anonymous fetch (a public repository).
func (s *Server) resolveSourceAuth(req *deploymentSource) (*source.Auth, error) {
	if req.CredentialID != "" {
		cred, err := s.credentialsManager.GetGenericCredential(req.CredentialID)
		if err != nil {
			return nil, fmt.Errorf("failed to load source credential: %w", err)
		}
		return &source.Auth{
			Username: cred.Data["username"],
			Token:    cred.Data["token"],
		}, nil
	}
	if req.Token != "" {
		return &source.Auth{Username: req.Username, Token: req.Token}, nil
	}
	return nil, nil
}

// recordDeploymentSource stores the source on the deployment's current metadata
// rather than the create request's, which is nil when the caller sent none.
func (s *Server) recordDeploymentSource(name string, cfg *models.SourceConfig) error {
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		return err
	}
	meta := deployment.Metadata
	if meta == nil {
		meta = &models.ServiceMetadata{Name: name}
	}
	meta.Source = cfg
	return s.manager.SaveMetadata(name, meta)
}

var errNoSource = errors.New("deployment was not created from a source")

// redeployFromSource refetches a deployment's recorded source (at a pinned
// revision when given), syncs it into the deployment directory and rebuilds.
func (s *Server) redeployFromSource(ctx context.Context, name string, actOpts actionOptions, runOpts []docker.RunOption, emit func(string)) (err error) {
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		return err
	}
	if deployment.Metadata == nil || deployment.Metadata.Source == nil {
		return errNoSource
	}
	meta := deployment.Metadata

	req := deploymentSourceFrom(meta.Source)
	req.Revision = actOpts.Revision

	actOpts.progress.phase(deploys.PhaseBuilding)
	emit(fmt.Sprintf("Fetching %s (%s)", req.Ref, describeRevision(req)))
	fetched, err := s.fetchDeploymentSourceWithLog(ctx, req, emit)
	if err != nil {
		return err
	}
	defer fetched.cleanup()
	actOpts.progress.fetched(fetched.revision, fetched.message)

	var compose string
	if fetched.builder == build.KindCompose {
		compose = fetched.composeContent
		if content, err := docker.EnsureContainerNames(compose, name); err == nil {
			compose = content
		}
		if current, _, err := s.manager.GetComposeFile(name); err == nil {
			if carried, err := docker.CarryExternalNetworks(current, compose); err == nil {
				compose = carried
			} else {
				emit("Warning: could not carry networks over: " + err.Error())
			}
		}
	} else {
		if compose, err = s.buildSourceImage(ctx, name, meta, fetched, emit); err != nil {
			return err
		}
	}
	if err := s.validateNewComposeContent(compose, name, nil, fetched.dir); err != nil {
		return fmt.Errorf("invalid compose content in source: %w", err)
	}

	if err := s.manager.UpdateDeploymentFromSource(name, fetched.dir, compose); err != nil {
		return err
	}
	emit("Source " + shortRevision(fetched.revision) + " synced, rebuilding")
	actOpts.progress.phase(deploys.PhaseDeploying)

	if _, err := s.manager.RebuildDeployment(name, runOpts...); err != nil {
		return err
	}

	if updated, err := s.manager.GetDeployment(name); err == nil && updated.Metadata != nil && updated.Metadata.Source != nil {
		updated.Metadata.Source.LastRevision = fetched.revision
		updated.Metadata.Source.Builder = string(fetched.builder)
		if err := s.manager.SaveMetadata(name, updated.Metadata); err != nil {
			emit("Warning: failed to record revision: " + err.Error())
		}
	}
	return nil
}

func describeRevision(req *deploymentSource) string {
	if req.Revision != "" {
		return "revision " + shortRevision(req.Revision)
	}
	if req.Branch != "" {
		return "branch " + req.Branch
	}
	return "default branch"
}

func shortRevision(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

func (s *Server) redeployDeployment(c *gin.Context) {
	name := c.Param("name")
	if !s.requireUnprotectedDeploymentAction(c, name, protectedActionRebuild) {
		return
	}
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if deployment.Metadata == nil || deployment.Metadata.Source == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errNoSource.Error()})
		return
	}
	s.enqueueDeploymentAction(c, "redeploy")
}

func (s *Server) listDeploymentDeploys(c *gin.Context) {
	records, err := s.deploysStore.List(c.Param("name"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deploys": records})
}

// getDeployLogs returns the output stored for one deploy.
func (s *Server) getDeployLogs(c *gin.Context) {
	name := c.Param("name")
	rec, err := s.deploysStore.Get(name, c.Param("id"))
	if errors.Is(err, deploys.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	output, err := s.deploysStore.ReadLog(name, rec.ID)
	if err != nil && !errors.Is(err, deploys.ErrNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deploy": rec, "output": output})
}

// rollbackDeployment redeploys the revision a past deploy fetched.
func (s *Server) rollbackDeployment(c *gin.Context) {
	name := c.Param("name")
	if !s.requireUnprotectedDeploymentAction(c, name, protectedActionRebuild) {
		return
	}
	rec, err := s.deploysStore.Get(name, c.Param("id"))
	if errors.Is(err, deploys.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if rec.Revision == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "this deploy has no recorded revision to roll back to"})
		return
	}
	s.enqueueDeploymentActionWith(c, "redeploy", actionOptions{
		Revision: rec.Revision,
		Trigger:  string(deploys.TriggerRollback),
	})
}

// sourceAppService is the service name the agent uses in compose files it
// writes for built images.
const sourceAppService = "app"

// sourceImageCompose writes the compose file for a deployment that runs an
// image built from its source. PORT tells the app where to listen, which is the
// convention Railpack-built apps follow.
func (s *Server) sourceImageCompose(name, image string, port int) (string, error) {
	if port == 0 {
		port = 8080
	}
	content, err := s.generateCustomCompose(&ComposeGenerateRequest{Name: name, Image: image, ContainerPort: port})
	if err != nil {
		return "", err
	}
	if content, err = docker.EnsureServiceEnvFile(content, ".env.flatrun"); err != nil {
		return "", err
	}
	return docker.SetServiceEnv(content, sourceAppService, "PORT", fmt.Sprintf("%d", port))
}

// buildSourceImage builds the fetched tree into an image tagged with its
// revision and returns the deployment's compose file pointed at that image.
func (s *Server) buildSourceImage(ctx context.Context, name string, meta *models.ServiceMetadata, fetched *fetchedSource, emit func(string)) (string, error) {
	scratch, err := os.MkdirTemp("", "flatrun-build-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	tag := build.ImageTag(name, fetched.revision)
	if err := s.builders.Build(ctx, fetched.builder, build.Request{Dir: fetched.dir, Scratch: scratch, Tag: tag}, emit); err != nil {
		return "", err
	}

	current, _, err := s.manager.GetComposeFile(name)
	if err != nil {
		return s.sourceImageCompose(name, tag, meta.Networking.ContainerPort)
	}
	service := sourceAppService
	if meta.Networking.Service != "" {
		service = meta.Networking.Service
	}
	return docker.SetServiceImage(current, service, tag)
}
