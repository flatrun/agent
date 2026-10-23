package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/flatrun/agent/internal/deploys"
	"github.com/flatrun/agent/internal/webhook"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

const maxWebhookBody = 5 << 20

// receiveWebhook is the public endpoint git hosts deliver to. It is
// authenticated only by the deployment's webhook secret, so every failure up
// to and including verification answers the same way and reveals nothing.
func (s *Server) receiveWebhook(c *gin.Context) {
	notFound := func() { c.JSON(http.StatusNotFound, gin.H{"error": "webhook not found"}) }

	provider, ok := s.webhooks.Get(c.Param("provider"))
	if !ok {
		notFound()
		return
	}
	name := c.Param("deployment")
	deployment, err := s.manager.GetDeployment(name)
	if err != nil || deployment.Metadata == nil || deployment.Metadata.Source == nil {
		notFound()
		return
	}
	src := deployment.Metadata.Source
	secret := s.webhookSecret(src)
	if secret == "" {
		notFound()
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxWebhookBody))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return
	}
	if err := provider.Verify(c.Request.Header, body, secret); err != nil {
		notFound()
		return
	}

	event, err := provider.Parse(c.Request.Header, body)
	if errors.Is(err, webhook.ErrUnsupported) {
		c.JSON(http.StatusOK, gin.H{"status": "ignored", "reason": "event type not handled"})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload: " + err.Error()})
		return
	}
	if !event.MatchesRepo(src.Ref) {
		c.JSON(http.StatusOK, gin.H{"status": "ignored", "reason": "repository does not match this deployment"})
		return
	}

	switch event.Kind {
	case webhook.EventPush:
		s.handlePushEvent(c, name, src, provider.Name(), event)
	case webhook.EventPROpened, webhook.EventPRSynced, webhook.EventPRClosed:
		s.handlePullRequestEvent(c, name, src, event)
	default:
		c.JSON(http.StatusOK, gin.H{"status": "ignored", "reason": "nothing to deploy for this event"})
	}
}

func (s *Server) handlePushEvent(c *gin.Context, name string, src *models.SourceConfig, provider string, event *webhook.Event) {
	// The generic hook is an explicit deploy request for this deployment; host
	// pushes deploy every target that follows the pushed branch and opted in.
	explicit := provider == "generic"
	targets := map[string]bool{}
	if explicit || (src.AutoDeploy && branchMatches(src.Branch, event.Branch)) {
		targets[name] = true
	}
	if !explicit {
		if envs, err := s.environmentsOf(name); err == nil {
			for _, env := range envs {
				envSrc := env.Metadata.Source
				if env.Metadata.Environment.PR == 0 && envSrc != nil && envSrc.AutoDeploy && branchMatches(envSrc.Branch, event.Branch) {
					targets[env.Name] = true
				}
			}
		}
	}
	if len(targets) == 0 {
		reason := "push is not to a deployed branch"
		if !src.AutoDeploy {
			reason = "auto deploy is off"
		}
		c.JSON(http.StatusOK, gin.H{"status": "ignored", "reason": reason})
		return
	}

	jobs := gin.H{}
	busy := gin.H{}
	for target := range targets {
		job, created := s.startDeploymentJob(target, "redeploy", actionOptions{
			Revision: event.Revision,
			Trigger:  string(deploys.TriggerPush),
		})
		if created {
			jobs[target] = job.ID()
		} else {
			busy[target] = job.ID()
		}
	}
	if len(jobs) == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "a deploy is already running", "active_jobs": busy})
		return
	}
	resp := gin.H{"status": "deploying", "jobs": jobs}
	if id, ok := jobs[name]; ok {
		resp["job_id"] = id
	}
	if len(busy) > 0 {
		resp["already_running"] = busy
	}
	c.JSON(http.StatusAccepted, resp)
}

// branchMatches treats an empty tracked branch as the repository default, which
// the agent cannot know, so it follows any pushed branch.
func branchMatches(tracked, pushed string) bool {
	return tracked == "" || pushed == "" || tracked == pushed
}

func (s *Server) webhookSecret(src *models.SourceConfig) string {
	if src.WebhookCredentialID == "" || s.credentialsManager == nil {
		return ""
	}
	cred, err := s.credentialsManager.GetGenericCredential(src.WebhookCredentialID)
	if err != nil {
		return ""
	}
	return cred.Data["secret"]
}

func webhookPaths(name string) gin.H {
	return gin.H{
		"github":  "/api/hooks/github/" + name,
		"gitlab":  "/api/hooks/gitlab/" + name,
		"generic": "/api/hooks/generic/" + name,
	}
}

func (s *Server) getDeploymentWebhook(c *gin.Context) {
	name := c.Param("name")
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if deployment.Metadata == nil || deployment.Metadata.Source == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errNoSource.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"paths":      webhookPaths(name),
		"configured": s.webhookSecret(deployment.Metadata.Source) != "",
	})
}

// rotateDeploymentWebhook creates a new webhook secret and returns it once.
// The previous secret stops working immediately.
func (s *Server) rotateDeploymentWebhook(c *gin.Context) {
	name := c.Param("name")
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	meta := deployment.Metadata
	if meta == nil || meta.Source == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errNoSource.Error()})
		return
	}

	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	secret := hex.EncodeToString(raw)
	data := map[string]string{"secret": secret}

	if meta.Source.WebhookCredentialID != "" {
		if _, err := s.credentialsManager.UpdateGenericCredential(meta.Source.WebhookCredentialID, "", data); err == nil {
			c.JSON(http.StatusOK, gin.H{"paths": webhookPaths(name), "secret": secret})
			return
		}
	}
	cred, err := s.credentialsManager.CreateGenericCredential("webhook-"+name+"-"+secret[:6], models.CredentialKindWebhook, data)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	meta.Source.WebhookCredentialID = cred.ID
	if err := s.manager.SaveMetadata(name, meta); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"paths": webhookPaths(name), "secret": secret})
}

// updateDeploymentSource changes how a deployment follows its repository.
func (s *Server) updateDeploymentSource(c *gin.Context) {
	name := c.Param("name")
	var req struct {
		Branch     *string `json:"branch"`
		AutoDeploy *bool   `json:"auto_deploy"`
		Previews   *bool   `json:"previews"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	meta := deployment.Metadata
	if meta == nil || meta.Source == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errNoSource.Error()})
		return
	}
	if req.Branch != nil {
		meta.Source.Branch = *req.Branch
	}
	if req.AutoDeploy != nil {
		meta.Source.AutoDeploy = *req.AutoDeploy
	}
	if req.Previews != nil {
		meta.Source.Previews = *req.Previews
	}
	if err := s.manager.SaveMetadata(name, meta); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("Deployment %s source updated: branch=%s auto_deploy=%v previews=%v", name, meta.Source.Branch, meta.Source.AutoDeploy, meta.Source.Previews)
	c.JSON(http.StatusOK, gin.H{"source": meta.Source})
}

func previewEnvironmentName(pr int) string {
	return fmt.Sprintf("pr-%d", pr)
}

// handlePullRequestEvent keeps one preview environment per open pull request:
// created when it opens, redeployed at each new head commit, deleted when it
// closes or merges. Work happens in the background because a first fetch and
// build outlast a git host's delivery timeout.
func (s *Server) handlePullRequestEvent(c *gin.Context, base string, src *models.SourceConfig, event *webhook.Event) {
	if !src.Previews {
		c.JSON(http.StatusOK, gin.H{"status": "ignored", "reason": "previews are off"})
		return
	}
	if event.PR <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "event has no pull request number"})
		return
	}
	envName := previewEnvironmentName(event.PR)
	name := environmentDeploymentName(base, envName)
	existing, err := s.manager.GetDeployment(name)
	exists := err == nil && existing.Metadata != nil && existing.Metadata.Environment != nil &&
		existing.Metadata.Environment.Base == base && existing.Metadata.Environment.PR == event.PR

	if event.Kind == webhook.EventPRClosed {
		if !exists {
			c.JSON(http.StatusOK, gin.H{"status": "ignored", "reason": "no preview to remove"})
			return
		}
		s.background(func() {
			hasProxy := s.proxyOrchestrator != nil
			if _, err := s.applyDeploymentDelete(name, deploymentDeleteOptions{DeleteSSL: hasProxy, DeleteVhost: hasProxy}); err != nil {
				log.Printf("Warning: failed to remove preview %s: %v", name, err)
			}
		})
		c.JSON(http.StatusAccepted, gin.H{"status": "removing", "name": name})
		return
	}

	if exists {
		if existing.Metadata.Source != nil && event.Branch != "" && existing.Metadata.Source.Branch != event.Branch {
			existing.Metadata.Source.Branch = event.Branch
			_ = s.manager.SaveMetadata(name, existing.Metadata)
		}
		job, created := s.startDeploymentJob(name, "redeploy", actionOptions{
			Revision: event.Revision,
			Trigger:  string(deploys.TriggerPR),
		})
		if !created {
			c.JSON(http.StatusConflict, gin.H{"error": "a deploy is already running", "active_job_id": job.ID()})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"status": "deploying", "name": name, "job_id": job.ID()})
		return
	}

	s.background(func() {
		_, _, err := s.createEnvironment(context.Background(), base, environmentRequest{
			Name:     envName,
			Branch:   event.Branch,
			Revision: event.Revision,
			PR:       event.PR,
			Trigger:  deploys.TriggerPR,
		})
		if err != nil {
			log.Printf("Warning: failed to create preview %s: %v", name, err)
		}
	})
	c.JSON(http.StatusAccepted, gin.H{"status": "creating", "name": name})
}

// background runs work that must outlive the request. Tests replace it to run
// the work inline.
func (s *Server) background(work func()) {
	if s.runInBackground != nil {
		s.runInBackground(work)
		return
	}
	go work()
}
