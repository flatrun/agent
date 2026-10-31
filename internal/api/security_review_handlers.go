package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/securityreview"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
	"github.com/flatrun/agent/pkg/securityprofile"
	"github.com/gin-gonic/gin"
)

func (s *Server) registerSecurityReviewRoutes(g *gin.RouterGroup) {
	read := s.authMiddleware.RequirePermission(auth.PermSecurityProfilesRead)
	write := s.authMiddleware.RequirePermission(auth.PermSecurityProfilesRead, auth.PermSecurityProfilesWrite)
	depRead := s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelRead)
	depWrite := s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelWrite)
	g.GET("/deployments/:name/security-profile", read, depRead, s.getSecurityProfile)
	g.PUT("/deployments/:name/security-profile", write, depWrite, s.putSecurityProfile)
	g.POST("/deployments/:name/security-profile/proposals", write, depWrite, s.proposeSecurityProfile)
	g.POST("/deployments/:name/security-profile/generate", write, s.authMiddleware.RequirePermission(auth.PermDeploymentsRead), depWrite, s.generateSecurityProfile)
	g.POST("/deployments/:name/security-profile/import", write, s.authMiddleware.RequirePermission(auth.PermTemplatesRead), depWrite, s.importSecurityProfile)
	g.GET("/deployments/:name/security-profile/proposals/:id", read, depRead, s.getSecurityProposal)
	g.POST("/deployments/:name/security-profile/proposals/:id/approve", s.authMiddleware.RequirePermission(auth.PermSecurityProfilesRead, auth.PermSecurityProfilesApprove), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelAdmin), s.approveSecurityProfile)
	g.GET("/deployments/:name/security-reviews", s.authMiddleware.RequirePermission(auth.PermSecurityReviewsRead), depRead, s.listSecurityReviews)
	g.GET("/deployments/:name/security-findings", s.authMiddleware.RequirePermission(auth.PermSecurityReviewsRead), depRead, s.listSecurityFindings)
	g.POST("/deployments/:name/security-findings/:id/close", s.authMiddleware.RequirePermission(auth.PermSecurityReviewsRead, auth.PermSecurityProfilesApprove), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelAdmin), s.closeSecurityFinding)
	g.POST("/deployments/:name/security-reviews", s.authMiddleware.RequirePermission(auth.PermSecurityReviewsRead, auth.PermSecurityReviewsRun), depWrite, s.runSecurityReview)
	g.GET("/deployments/:name/security-reviews/:id", s.authMiddleware.RequirePermission(auth.PermSecurityReviewsRead), depRead, s.getSecurityReview)
	g.PUT("/deployments/:name/security-reviews/:id/hold", s.authMiddleware.RequirePermission(auth.PermSecurityReviewsRead, auth.PermSecurityProfilesApprove), s.authMiddleware.RequireDeploymentAccess(auth.AccessLevelAdmin), s.holdSecurityReview)
	g.GET("/deployments/:name/security-reviews/:id/evidence/:evidence_id", s.authMiddleware.RequirePermission(auth.PermSecurityReviewsRead, auth.PermSecurityEvidenceRead), depRead, s.getSecurityEvidence)
}

func (s *Server) reviewStore(c *gin.Context) *securityreview.Store {
	s.securityReviewMu.Lock()
	defer s.securityReviewMu.Unlock()
	if s.securityReviewStore != nil {
		return s.securityReviewStore
	}
	if s.config.SecurityReviews.StatePath == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Security review storage is not configured outside deployment storage", "status": "unsupported"})
		return nil
	}
	store, err := securityreview.NewStore(s.config.SecurityReviews.StatePath, s.config.DeploymentsPath)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Private security review storage unavailable", "status": "unsupported"})
		return nil
	}
	s.securityReviewStore = store
	return store
}
func (s *Server) securityDiscovery(c *gin.Context) (*models.Deployment, securityreview.Discovery, bool) {
	actor := auth.GetActorFromContext(c)
	if actor == nil || !securityprofile.ValidIdentifier(c.Param("name")) || !actor.CanAccessDeployment(c.Param("name"), auth.AccessLevelRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Deployment access required"})
		return nil, securityreview.Discovery{}, false
	}
	d, err := s.manager.GetDeployment(c.Param("name"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return nil, securityreview.Discovery{}, false
	}
	discovery, err := securityreview.Discover(d)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Deployment discovery unavailable"})
		return nil, discovery, false
	}
	return d, discovery, true
}
func securityError(c *gin.Context, err error) {
	status := http.StatusUnprocessableEntity
	if errors.Is(err, securityreview.ErrConflict) {
		status = http.StatusConflict
	}
	if errors.Is(err, securityreview.ErrNotFound) {
		status = http.StatusNotFound
	}
	c.JSON(status, gin.H{"error": err.Error()})
}

func (s *Server) managementSecurityPreview(c *gin.Context) {
	c.JSON(http.StatusOK, config.PreviewManagement(s.config))
}
func securityJSON(c *gin.Context, value any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 512<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "Invalid security request"})
		return false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "Unexpected trailing request data"})
		return false
	}
	return true
}

func (s *Server) getSecurityProfile(c *gin.Context) {
	d, discovery, ok := s.securityDiscovery(c)
	if !ok {
		return
	}
	store := s.reviewStore(c)
	if store == nil {
		return
	}
	state, err := store.State(d.Name)
	if err != nil {
		securityError(c, err)
		return
	}
	if state.Profile == nil && d.Metadata != nil && d.Metadata.SecurityProfile != nil {
		state.Profile = d.Metadata.SecurityProfile
		state.Status = "draft"
	}
	if state.Profile != nil {
		if err := state.Profile.Validate(); err != nil {
			state.Status = "unsupported"
		}
	}
	if state.Approval != nil && (state.Approval.DeploymentRevision != discovery.Revision || state.Approval.ProfileRevision != state.Revision) {
		state.Status = "outdated"
	}
	ready, err := store.ReadyProposals(d.Name, state.Revision, discovery.Revision)
	if err != nil {
		securityError(c, err)
		return
	}
	warnings := append([]string{"Application authorization coverage is unknown until approved historical audit evidence is connected", "Schedule intervals are targets pending capacity measurement"}, config.PreviewManagement(s.config).Warnings...)
	c.JSON(http.StatusOK, gin.H{"ready_proposals": ready, "assistant_available": s.aiProvider != nil, "state": state, "suggested_profile": securityreview.DiscoveredDraft("unknown", discovery), "discovery": discovery, "baseline_version": securityprofile.BaselineVersion, "warnings": warnings})
}

type securityProfileRequest struct {
	BaseRevision string                  `json:"base_revision"`
	Profile      securityprofile.Profile `json:"profile"`
}

func (s *Server) putSecurityProfile(c *gin.Context) {
	d, _, ok := s.securityDiscovery(c)
	if !ok {
		return
	}
	store := s.reviewStore(c)
	if store == nil {
		return
	}
	var req securityProfileRequest
	if !securityJSON(c, &req) {
		return
	}
	state, err := store.Draft(d.Name, req.BaseRevision, req.Profile)
	if err != nil {
		securityError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"state": state})
}
func (s *Server) proposeSecurityProfile(c *gin.Context) {
	d, discovery, ok := s.securityDiscovery(c)
	if !ok {
		return
	}
	store := s.reviewStore(c)
	if store == nil {
		return
	}
	var req securityProfileRequest
	if !securityJSON(c, &req) {
		return
	}
	p, err := store.Propose(d.Name, req.BaseRevision, discovery.Revision, "operator", req.Profile)
	if err != nil {
		securityError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"proposal": p})
}
func (s *Server) getSecurityProposal(c *gin.Context) {
	d, _, ok := s.securityDiscovery(c)
	if !ok {
		return
	}
	store := s.reviewStore(c)
	if store == nil {
		return
	}
	p, err := store.Proposal(d.Name, c.Param("id"))
	if err != nil {
		securityError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"proposal": p})
}
func (s *Server) approveSecurityProfile(c *gin.Context) {
	actor := auth.GetActorFromContext(c)
	if actor == nil || (actor.Type != "user" && actor.Type != "api_key") || (actor.Role == auth.RoleService || (actor.User != nil && actor.User.Role == auth.RoleService)) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Authenticated owner approval required"})
		return
	}
	d, discovery, ok := s.securityDiscovery(c)
	if !ok {
		return
	}
	store := s.reviewStore(c)
	if store == nil {
		return
	}
	var req struct {
		BaseRevision       string `json:"base_revision"`
		DeploymentRevision string `json:"deployment_revision"`
		ProfileRevision    string `json:"profile_revision"`
	}
	if !securityJSON(c, &req) {
		return
	}
	if req.DeploymentRevision != discovery.Revision {
		securityError(c, securityreview.ErrConflict)
		return
	}
	state, err := store.Approve(d.Name, c.Param("id"), req.BaseRevision, req.DeploymentRevision, req.ProfileRevision, securityOwnerID(actor))
	if err != nil {
		securityError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"state": state})
}
func (s *Server) importSecurityProfile(c *gin.Context) {
	d, discovery, ok := s.securityDiscovery(c)
	if !ok {
		return
	}
	store := s.reviewStore(c)
	if store == nil {
		return
	}
	var req struct {
		BaseRevision string `json:"base_revision"`
	}
	if !securityJSON(c, &req) {
		return
	}
	if d.Metadata == nil || !securityprofile.ValidIdentifier(d.Metadata.Template) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "No source template recorded"})
		return
	}
	if s.templateSyncer != nil {
		s.templateSyncer.SyncIfStale(c.Request.Context(), 0)
	}
	metadata, err := s.loadTemplateMetadata(d.Metadata.Template)
	if err != nil || metadata.SecurityProfile == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Source template has no security profile"})
		return
	}
	state, err := store.State(d.Name)
	if err != nil {
		securityError(c, err)
		return
	}
	imported := *metadata.SecurityProfile
	if err := imported.Validate(); err != nil {
		securityError(c, err)
		return
	}
	if imported.Reviews == nil {
		imported.Reviews = map[string]securityprofile.Review{}
	}
	if imported.Targets == nil {
		imported.Targets = map[string]securityprofile.Target{}
	}
	if state.Profile == nil && d.Metadata != nil {
		state.Profile = d.Metadata.SecurityProfile
	}
	if state.Profile != nil {
		data, _ := json.Marshal(state.Profile)
		var local securityprofile.Profile
		_ = json.Unmarshal(data, &local)
		for id, review := range local.Reviews {
			if incoming, exists := imported.Reviews[id]; exists {
				incoming.Expectations = review.Expectations
				imported.Reviews[id] = incoming
			} else {
				imported.Reviews[id] = review
			}
		}
		for id, target := range local.Targets {
			imported.Targets[id] = target
		}
		imported.AccessPolicy = local.AccessPolicy
		imported.Exceptions = local.Exceptions
		imported.Schedule = local.Schedule
		imported.Budget = local.Budget
	}
	p, err := store.Propose(d.Name, req.BaseRevision, discovery.Revision, "template:"+d.Metadata.Template, imported)
	if err != nil {
		securityError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"proposal": p})
}
func (s *Server) generateSecurityProfile(c *gin.Context) {
	d, discovery, ok := s.securityDiscovery(c)
	if !ok {
		return
	}
	store := s.reviewStore(c)
	if store == nil {
		return
	}
	if s.aiProvider == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Profile assistance requires a configured model"})
		return
	}
	var req struct {
		BaseRevision string `json:"base_revision"`
	}
	if !securityJSON(c, &req) {
		return
	}
	state, err := store.State(d.Name)
	if err != nil {
		securityError(c, err)
		return
	}
	if state.Revision != req.BaseRevision {
		securityError(c, securityreview.ErrConflict)
		return
	}
	draft := securityreview.DiscoveredDraft("unknown", discovery)
	if state.Profile != nil {
		draft = *state.Profile
	} else if d.Metadata != nil && d.Metadata.SecurityProfile != nil {
		draft = *d.Metadata.SecurityProfile
	}
	if err = draft.Validate(); err != nil {
		securityError(c, err)
		return
	}
	bundle, err := json.Marshal(securityreview.Sanitize(struct {
		Discovery securityreview.Discovery `json:"discovery"`
		Profile   securityprofile.Profile  `json:"profile"`
		Images    []string                 `json:"approved_images"`
	}{discovery, draft, s.config.SecurityReviews.ApprovedImages}))
	if err != nil || len(bundle) > draft.Budget.ModelInputBytes {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "Discovery exceeds model budget"})
		return
	}
	s.securityReviewMu.Lock()
	if s.securityReviewContext == nil {
		s.securityReviewContext, s.securityReviewCancel = context.WithCancel(context.Background())
	}
	if s.securityProposalSlots == nil {
		s.securityProposalSlots = make(chan struct{}, 2)
	}
	parent := s.securityReviewContext
	slots := s.securityProposalSlots
	s.securityReviewMu.Unlock()
	select {
	case slots <- struct{}{}:
	default:
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Profile generation capacity is occupied"})
		return
	}
	proposal, err := store.QueueProposal(d.Name, req.BaseRevision, discovery.Revision, draft)
	if err != nil {
		<-slots
		securityError(c, err)
		return
	}
	provider := s.aiProvider
	go func() {
		defer func() { <-slots }()
		ctx, done := context.WithTimeout(parent, 120*time.Second)
		defer done()
		profile, generationErr := securityreview.GenerateProfile(ctx, provider, bundle, draft)
		_ = store.FinishProposal(d.Name, proposal.ID, profile, generationErr)
	}()
	c.JSON(http.StatusAccepted, gin.H{"proposal_id": proposal.ID, "proposal": proposal})
}

func (s *Server) listSecurityReviews(c *gin.Context) {
	d, _, ok := s.securityDiscovery(c)
	if !ok {
		return
	}
	store := s.reviewStore(c)
	if store == nil {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit < 1 || limit > 100 || offset < 0 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "Invalid pagination"})
		return
	}
	reports, err := store.Reports(d.Name, limit, offset)
	if err != nil {
		securityError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"reports": reports, "limit": limit, "offset": offset})
}
func (s *Server) getSecurityReview(c *gin.Context) {
	d, _, ok := s.securityDiscovery(c)
	if !ok {
		return
	}
	store := s.reviewStore(c)
	if store == nil {
		return
	}
	r, err := store.Report(d.Name, c.Param("id"))
	if err != nil {
		securityError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"report": r})
}

func (s *Server) listSecurityFindings(c *gin.Context) {
	d, _, ok := s.securityDiscovery(c)
	if !ok {
		return
	}
	store := s.reviewStore(c)
	if store == nil {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit < 1 || limit > 100 || offset < 0 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "Invalid pagination"})
		return
	}
	findings, err := store.Findings(d.Name, limit, offset)
	if err != nil {
		securityError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"findings": findings, "limit": limit, "offset": offset})
}
func (s *Server) closeSecurityFinding(c *gin.Context) {
	actor := auth.GetActorFromContext(c)
	if actor == nil || (actor.Type != "user" && actor.Type != "api_key") || (actor.Role == auth.RoleService || (actor.User != nil && actor.User.Role == auth.RoleService)) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Owner closure required"})
		return
	}
	d, _, ok := s.securityDiscovery(c)
	if !ok {
		return
	}
	store := s.reviewStore(c)
	if store == nil {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if !securityJSON(c, &req) {
		return
	}
	if err := store.CloseFinding(d.Name, c.Param("id"), req.Reason, securityOwnerID(actor)); err != nil {
		securityError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "closed"})
}
func (s *Server) getSecurityEvidence(c *gin.Context) {
	d, _, ok := s.securityDiscovery(c)
	if !ok {
		return
	}
	store := s.reviewStore(c)
	if store == nil {
		return
	}
	e, err := store.Evidence(d.Name, c.Param("id"), c.Param("evidence_id"))
	if err != nil {
		securityError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"evidence": e})
}

func (s *Server) holdSecurityReview(c *gin.Context) {
	actor := auth.GetActorFromContext(c)
	if actor == nil || (actor.Role == auth.RoleService || (actor.User != nil && actor.User.Role == auth.RoleService)) || (actor.Type != "user" && actor.Type != "api_key") {
		c.JSON(http.StatusForbidden, gin.H{"error": "Owner approval required"})
		return
	}
	d, _, ok := s.securityDiscovery(c)
	if !ok {
		return
	}
	store := s.reviewStore(c)
	if store == nil {
		return
	}
	var req struct {
		Hold *bool `json:"hold"`
	}
	if !securityJSON(c, &req) {
		return
	}
	if req.Hold == nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "Hold is required"})
		return
	}
	if err := store.Hold(d.Name, c.Param("id"), *req.Hold); err != nil {
		securityError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"hold": *req.Hold})
}

func (s *Server) reviewEngine(store *securityreview.Store) (*securityreview.Engine, error) {
	s.securityReviewMu.Lock()
	defer s.securityReviewMu.Unlock()
	if s.securityReviewEngine != nil {
		return s.securityReviewEngine, nil
	}
	keys, err := securityreview.OpenEvidenceKeys(s.config.SecurityReviews.StatePath)
	if err != nil {
		return nil, err
	}
	engine := securityreview.NewEngine(store, securityreview.Runner{Images: s.config.SecurityReviews.ApprovedImages, ScratchRoot: filepath.Join(s.config.SecurityReviews.StatePath, "snapshots"), DockerHost: s.config.DockerSocket}, keys, s.manager.GetDeployment)
	engine.ModelEnabled = s.aiProvider != nil
	engine.DeliveryEnabled = s.config.SecurityReviews.Delivery.URL != ""
	if err = engine.Prepare(); err != nil {
		return nil, err
	}
	if s.securityReviewContext == nil {
		s.securityReviewContext, s.securityReviewCancel = context.WithCancel(context.Background())
	}
	ctx := s.securityReviewContext
	s.securityReviewEngine = engine
	if engine.DeliveryEnabled {
		settings := s.config.SecurityReviews.Delivery
		delivery := securityreview.Delivery{URL: settings.URL, AllowedHosts: settings.AllowedHosts, Credential: func() (string, error) {
			if s.credentialsManager == nil || settings.CredentialID == "" {
				return "", fmt.Errorf("delivery credential unavailable")
			}
			credential, err := s.credentialsManager.GetCredential(settings.CredentialID)
			if err != nil {
				return "", err
			}
			return credential.Password, nil
		}}
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					pending, err := store.DeliveryPending()
					if err != nil {
						continue
					}
					for _, report := range pending {
						deliveryCtx, done := context.WithTimeout(ctx, 2*time.Minute)
						err = store.Deliver(deliveryCtx, delivery, report)
						done()
						if err != nil {
							_ = store.DeliveryFailed(report.Deployment, report.ID)
						}
					}
				}
			}
		}()
	}
	if s.aiProvider != nil {
		provider := s.aiProvider
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					pending, err := store.ModelPending(2)
					if err != nil {
						continue
					}
					for _, report := range pending {
						modelCtx, done := context.WithTimeout(ctx, 120*time.Second)
						err = store.Assess(modelCtx, provider, report)
						done()
						if err != nil {
							_ = store.ModelUnavailable(report.Deployment, report.ID)
						}
					}
				}
			}
		}()
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			_ = engine.Schedule(time.Now())
			_ = engine.Poll(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return engine, nil
}
func (s *Server) runSecurityReview(c *gin.Context) {
	d, discovery, ok := s.securityDiscovery(c)
	if !ok {
		return
	}
	store := s.reviewStore(c)
	if store == nil {
		return
	}
	var req struct {
		Mode               string `json:"mode"`
		ProfileRevision    string `json:"profile_revision"`
		DeploymentRevision string `json:"deployment_revision"`
	}
	if !securityJSON(c, &req) {
		return
	}
	if req.DeploymentRevision != discovery.Revision {
		securityError(c, securityreview.ErrConflict)
		return
	}
	engine, err := s.reviewEngine(store)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Isolated review engine unavailable"})
		return
	}
	r, err := engine.Queue(d.Name, req.Mode, req.ProfileRevision, req.DeploymentRevision)
	if err != nil {
		securityError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"run_id": r.ID, "report": r})
}

func securityOwnerID(actor *auth.ActorContext) string {
	if actor.APIKey != nil {
		return fmt.Sprintf("user:%d/key:%s", actor.UserID, actor.APIKey.KeyID)
	}
	return fmt.Sprintf("user:%d", actor.UserID)
}
