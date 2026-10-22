package api

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/secrets"
	"github.com/flatrun/agent/internal/workspace"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

const workspaceHeader = "X-Workspace"

func (s *Server) registerWorkspaceRoutes(r *gin.RouterGroup) {
	read := s.authMiddleware.RequirePermission(auth.PermWorkspacesRead)
	write := s.authMiddleware.RequirePermission(auth.PermWorkspacesWrite)

	r.GET("/workspaces", read, s.listWorkspaces)
	r.POST("/workspaces", write, s.createWorkspace)
	r.POST("/workspaces/invitations/accept", read, s.acceptWorkspaceInvitation)
	r.GET("/workspaces/:slug", read, s.getWorkspace)
	r.PUT("/workspaces/:slug", write, s.updateWorkspace)
	r.DELETE("/workspaces/:slug", write, s.deleteWorkspace)
	r.GET("/workspaces/:slug/members", read, s.listWorkspaceMembers)
	r.POST("/workspaces/:slug/members", write, s.addWorkspaceMember)
	r.PUT("/workspaces/:slug/members/:user_id", write, s.updateWorkspaceMember)
	r.DELETE("/workspaces/:slug/members/:user_id", read, s.removeWorkspaceMember)
	r.GET("/workspaces/:slug/invitations", read, s.listWorkspaceInvitations)
	r.POST("/workspaces/:slug/invitations", write, s.createWorkspaceInvitation)
	r.DELETE("/workspaces/:slug/invitations/:id", write, s.revokeWorkspaceInvitation)
	r.GET("/workspaces/:slug/resources", read, s.listWorkspaceResources)
	r.PUT("/workspaces/:slug/resources", write, s.moveWorkspaceResource)
}

func (s *Server) workspaces() *workspace.Store {
	if s.authManager == nil {
		return nil
	}
	return s.authManager.Workspaces()
}

func isGlobalAdmin(actor *auth.ActorContext) bool {
	if actor == nil {
		return false
	}
	if actor.User != nil {
		return actor.User.Role == auth.RoleAdmin && (actor.APIKey == nil || actor.Role == auth.RoleAdmin)
	}
	return actor.Role == auth.RoleAdmin
}

// workspaceRole is the actor's role in a workspace. Global admins act as
// owners of every workspace.
func (s *Server) workspaceRole(actor *auth.ActorContext, w *workspace.Workspace) (string, error) {
	if isGlobalAdmin(actor) {
		return workspace.RoleOwner, nil
	}
	if actor == nil || actor.User == nil {
		return "", nil
	}
	return s.workspaces().Role(w.ID, actor.User.ID)
}

func workspaceError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, workspace.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, workspace.ErrSlugTaken), errors.Is(err, workspace.ErrNotEmpty),
		errors.Is(err, workspace.ErrLastOwner), errors.Is(err, workspace.ErrProtected):
		status = http.StatusConflict
	case errors.Is(err, workspace.ErrInvalidSlug), errors.Is(err, workspace.ErrInvalidRole),
		errors.Is(err, workspace.ErrInvalidType), errors.Is(err, workspace.ErrInvitationInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, workspace.ErrInvitationExpired):
		status = http.StatusGone
	case errors.Is(err, workspace.ErrInviteeMismatch):
		status = http.StatusForbidden
	}
	c.JSON(status, gin.H{"error": err.Error()})
}

// loadWorkspace resolves :slug and checks the actor holds at least minRole
// there. Non-members get 404 so that workspace names do not leak.
func (s *Server) loadWorkspace(c *gin.Context, minRole string) (*workspace.Workspace, string, bool) {
	store := s.workspaces()
	if store == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "workspaces need authentication to be enabled"})
		return nil, "", false
	}
	w, err := store.Get(c.Param("slug"))
	if err != nil {
		workspaceError(c, err)
		return nil, "", false
	}
	role, err := s.workspaceRole(auth.GetActorFromContext(c), w)
	if err != nil {
		workspaceError(c, err)
		return nil, "", false
	}
	if role == "" {
		workspaceError(c, workspace.ErrNotFound)
		return nil, "", false
	}
	if workspace.RoleRank(role) < workspace.RoleRank(minRole) {
		c.JSON(http.StatusForbidden, gin.H{"error": "your workspace role does not allow this"})
		return nil, "", false
	}
	return w, role, true
}

// requestWorkspace is the workspace the request is scoped to through the
// X-Workspace header, or nil when the header is absent.
func (s *Server) requestWorkspace(c *gin.Context) (*workspace.Workspace, bool) {
	slug := strings.TrimSpace(c.GetHeader(workspaceHeader))
	if slug == "" || s.workspaces() == nil {
		return nil, true
	}
	w, err := s.workspaces().Get(slug)
	if err != nil {
		workspaceError(c, err)
		return nil, false
	}
	role, err := s.workspaceRole(auth.GetActorFromContext(c), w)
	if err != nil {
		workspaceError(c, err)
		return nil, false
	}
	if role == "" {
		workspaceError(c, workspace.ErrNotFound)
		return nil, false
	}
	return w, true
}

// creationWorkspace is where a resource created by this request belongs: the
// requested workspace, else the actor's personal workspace, else default.
func (s *Server) creationWorkspace(c *gin.Context) (*workspace.Workspace, bool) {
	store := s.workspaces()
	if store == nil {
		return nil, true
	}
	w, ok := s.requestWorkspace(c)
	if !ok || w != nil {
		return w, ok
	}
	if actor := auth.GetActorFromContext(c); actor != nil && actor.User != nil && !isGlobalAdmin(actor) {
		if personal, err := store.PersonalFor(actor.User.ID); err == nil {
			return personal, true
		}
	}
	def, err := store.EnsureDefault(nil)
	if err != nil {
		workspaceError(c, err)
		return nil, false
	}
	return def, true
}

func (s *Server) assignDeploymentWorkspace(w *workspace.Workspace, name string) {
	if w == nil || s.workspaces() == nil {
		return
	}
	if err := s.workspaces().Assign(w.ID, workspace.Ref{Type: workspace.TypeDeployment, ID: name}); err != nil {
		log.Printf("Warning: failed to assign %s to workspace %s: %v", name, w.Slug, err)
		return
	}
	s.recordDeploymentWorkspace(name, w.Slug)
}

// recordDeploymentWorkspace mirrors ownership into service.yml so that a
// deployment directory still says where it belongs when read on its own.
func (s *Server) recordDeploymentWorkspace(name, slug string) {
	if s.manager == nil {
		return
	}
	d, err := s.manager.GetDeployment(name)
	if err != nil || d.Metadata == nil || d.Metadata.Workspace == slug {
		return
	}
	d.Metadata.Workspace = slug
	if err := s.manager.SaveMetadata(name, d.Metadata); err != nil {
		log.Printf("Warning: failed to record workspace for %s: %v", name, err)
	}
}

// deploymentOwners maps every deployment to its workspace slug. Deployments
// without an owner, such as directories added by hand, are adopted by the
// workspace their service.yml names, or else by the default workspace.
func (s *Server) deploymentOwners(deployments []models.Deployment) map[string]string {
	store := s.workspaces()
	if store == nil {
		return nil
	}
	owners, err := store.Owners(workspace.TypeDeployment)
	if err != nil {
		log.Printf("Warning: failed to read deployment owners: %v", err)
		return nil
	}
	for _, d := range deployments {
		if _, ok := owners[d.Name]; ok {
			continue
		}
		target, err := store.Get(workspace.DefaultSlug)
		if d.Metadata != nil && d.Metadata.Workspace != "" {
			if named, namedErr := store.Get(d.Metadata.Workspace); namedErr == nil {
				target, err = named, nil
			}
		}
		if err != nil {
			continue
		}
		if err := store.Assign(target.ID, workspace.Ref{Type: workspace.TypeDeployment, ID: d.Name}); err == nil {
			owners[d.Name] = target.Slug
		}
	}
	return owners
}

func (s *Server) listWorkspaces(c *gin.Context) {
	store := s.workspaces()
	if store == nil {
		c.JSON(http.StatusOK, NewList([]workspace.Membership{}, ""))
		return
	}
	actor := auth.GetActorFromContext(c)
	var memberships []workspace.Membership
	var err error
	if actor != nil && actor.User != nil {
		memberships, err = store.ListForUser(actor.User.ID)
		if err != nil {
			workspaceError(c, err)
			return
		}
	}
	if isGlobalAdmin(actor) {
		seen := map[int64]bool{}
		for _, m := range memberships {
			seen[m.ID] = true
		}
		all, err := store.List()
		if err != nil {
			workspaceError(c, err)
			return
		}
		for _, w := range all {
			if !seen[w.ID] {
				memberships = append(memberships, workspace.Membership{Workspace: w, Role: workspace.RoleOwner})
			}
		}
	}
	c.JSON(http.StatusOK, NewList(memberships, ""))
}

func (s *Server) createWorkspace(c *gin.Context) {
	actor := auth.GetActorFromContext(c)
	if s.workspaces() == nil || actor == nil || actor.User == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "only signed-in users can create workspaces"})
		return
	}
	var req struct {
		Name string `json:"name" binding:"required"`
		Slug string `json:"slug"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	w, err := s.workspaces().Create(strings.TrimSpace(req.Slug), req.Name, false, actor.User.ID)
	if err != nil {
		workspaceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, workspace.Membership{Workspace: *w, Role: workspace.RoleOwner})
}

func (s *Server) getWorkspace(c *gin.Context) {
	w, role, ok := s.loadWorkspace(c, workspace.RoleMember)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, workspace.Membership{Workspace: *w, Role: role})
}

func (s *Server) updateWorkspace(c *gin.Context) {
	w, role, ok := s.loadWorkspace(c, workspace.RoleAdmin)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updated, err := s.workspaces().Rename(w.Slug, req.Name)
	if err != nil {
		workspaceError(c, err)
		return
	}
	c.JSON(http.StatusOK, workspace.Membership{Workspace: *updated, Role: role})
}

func (s *Server) deleteWorkspace(c *gin.Context) {
	w, _, ok := s.loadWorkspace(c, workspace.RoleOwner)
	if !ok {
		return
	}
	if err := s.workspaces().Delete(w.Slug); err != nil {
		workspaceError(c, err)
		return
	}
	if s.secretStore != nil {
		if err := s.secretStore.DropScope(secrets.Scope{Type: secrets.ScopeWorkspace, ID: w.Slug}); err != nil {
			log.Printf("Warning: failed to remove secrets of workspace %s: %v", w.Slug, err)
		}
	}
	c.Status(http.StatusNoContent)
}

type workspaceMemberView struct {
	workspace.Member
	Username string `json:"username"`
	Email    string `json:"email,omitempty"`
}

func (s *Server) listWorkspaceMembers(c *gin.Context) {
	w, _, ok := s.loadWorkspace(c, workspace.RoleMember)
	if !ok {
		return
	}
	members, err := s.workspaces().Members(w.ID)
	if err != nil {
		workspaceError(c, err)
		return
	}
	views := make([]workspaceMemberView, 0, len(members))
	for _, m := range members {
		view := workspaceMemberView{Member: m}
		if u, err := s.authManager.GetUser(m.UserID); err == nil {
			view.Username = u.Username
			view.Email = u.Email
		}
		views = append(views, view)
	}
	c.JSON(http.StatusOK, NewList(views, ""))
}

// canGrant says whether an actor holding role may give or change grant. Only
// owners hand out or change ownership.
func canGrant(role, grant, current string) bool {
	if workspace.RoleRank(role) < workspace.RoleRank(workspace.RoleAdmin) {
		return false
	}
	if role == workspace.RoleOwner {
		return true
	}
	return grant != workspace.RoleOwner && current != workspace.RoleOwner
}

func (s *Server) addWorkspaceMember(c *gin.Context) {
	w, role, ok := s.loadWorkspace(c, workspace.RoleAdmin)
	if !ok {
		return
	}
	var req struct {
		Username string `json:"username" binding:"required"`
		Role     string `json:"role" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	user, err := s.authManager.GetUserByUsername(req.Username)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	current, err := s.workspaces().Role(w.ID, user.ID)
	if err != nil {
		workspaceError(c, err)
		return
	}
	if !canGrant(role, req.Role, current) {
		c.JSON(http.StatusForbidden, gin.H{"error": "only owners can grant or change ownership"})
		return
	}
	if err := s.workspaces().SetMember(w.ID, user.ID, req.Role); err != nil {
		workspaceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, workspaceMemberView{Member: workspace.Member{UserID: user.ID, Role: req.Role}, Username: user.Username, Email: user.Email})
}

func memberID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user id"})
		return 0, false
	}
	return id, true
}

func (s *Server) updateWorkspaceMember(c *gin.Context) {
	w, role, ok := s.loadWorkspace(c, workspace.RoleAdmin)
	if !ok {
		return
	}
	userID, ok := memberID(c)
	if !ok {
		return
	}
	var req struct {
		Role string `json:"role" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	current, err := s.workspaces().Role(w.ID, userID)
	if err != nil {
		workspaceError(c, err)
		return
	}
	if current == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "not a member of this workspace"})
		return
	}
	if !canGrant(role, req.Role, current) {
		c.JSON(http.StatusForbidden, gin.H{"error": "only owners can grant or change ownership"})
		return
	}
	if err := s.workspaces().SetMember(w.ID, userID, req.Role); err != nil {
		workspaceError(c, err)
		return
	}
	c.JSON(http.StatusOK, workspace.Member{UserID: userID, Role: req.Role})
}

// removeWorkspaceMember lets admins remove members and lets anyone leave.
func (s *Server) removeWorkspaceMember(c *gin.Context) {
	w, role, ok := s.loadWorkspace(c, workspace.RoleMember)
	if !ok {
		return
	}
	userID, ok := memberID(c)
	if !ok {
		return
	}
	actor := auth.GetActorFromContext(c)
	leaving := actor != nil && actor.User != nil && actor.User.ID == userID
	if !leaving {
		current, err := s.workspaces().Role(w.ID, userID)
		if err != nil {
			workspaceError(c, err)
			return
		}
		if !canGrant(role, workspace.RoleMember, current) {
			c.JSON(http.StatusForbidden, gin.H{"error": "your workspace role does not allow this"})
			return
		}
	}
	if err := s.workspaces().RemoveMember(w.ID, userID); err != nil {
		workspaceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) listWorkspaceInvitations(c *gin.Context) {
	w, _, ok := s.loadWorkspace(c, workspace.RoleAdmin)
	if !ok {
		return
	}
	invitations, err := s.workspaces().Invitations(w.ID)
	if err != nil {
		workspaceError(c, err)
		return
	}
	c.JSON(http.StatusOK, NewList(invitations, ""))
}

func (s *Server) createWorkspaceInvitation(c *gin.Context) {
	w, role, ok := s.loadWorkspace(c, workspace.RoleAdmin)
	if !ok {
		return
	}
	var req struct {
		Invitee string `json:"invitee" binding:"required"`
		Role    string `json:"role" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !canGrant(role, req.Role, "") {
		c.JSON(http.StatusForbidden, gin.H{"error": "only owners can invite owners"})
		return
	}
	var inviter int64
	if actor := auth.GetActorFromContext(c); actor != nil && actor.User != nil {
		inviter = actor.User.ID
	}
	inv, token, err := s.workspaces().Invite(w.ID, req.Invitee, req.Role, inviter, 0)
	if err != nil {
		workspaceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"invitation": inv, "token": token})
}

func (s *Server) revokeWorkspaceInvitation(c *gin.Context) {
	w, _, ok := s.loadWorkspace(c, workspace.RoleAdmin)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid invitation id"})
		return
	}
	if err := s.workspaces().RevokeInvitation(w.ID, id); err != nil {
		workspaceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) acceptWorkspaceInvitation(c *gin.Context) {
	actor := auth.GetActorFromContext(c)
	if s.workspaces() == nil || actor == nil || actor.User == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "sign in as the invited user to accept"})
		return
	}
	var req struct {
		Token string `json:"token" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	m, err := s.workspaces().Accept(req.Token, actor.User.ID, actor.User.Username, actor.User.Email)
	if err != nil {
		workspaceError(c, err)
		return
	}
	c.JSON(http.StatusOK, m)
}

func (s *Server) listWorkspaceResources(c *gin.Context) {
	w, _, ok := s.loadWorkspace(c, workspace.RoleMember)
	if !ok {
		return
	}
	if s.manager != nil {
		if deployments, err := s.manager.ListDeployments(); err == nil {
			s.deploymentOwners(deployments)
		}
	}
	refs, err := s.workspaces().ResourceRefs(w.ID)
	if err != nil {
		workspaceError(c, err)
		return
	}
	if t := c.Query("type"); t != "" {
		filtered := []workspace.Ref{}
		for _, r := range refs {
			if r.Type == t {
				filtered = append(filtered, r)
			}
		}
		refs = filtered
	}
	c.JSON(http.StatusOK, NewList(refs, ""))
}

// moveWorkspaceResource moves a resource into this workspace. The actor must
// administer both the target workspace and the resource itself.
func (s *Server) moveWorkspaceResource(c *gin.Context) {
	w, _, ok := s.loadWorkspace(c, workspace.RoleAdmin)
	if !ok {
		return
	}
	var req workspace.Ref
	if err := c.ShouldBindJSON(&req); err != nil || req.ID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "type and id are required"})
		return
	}
	if !workspace.ValidResourceType(req.Type) {
		workspaceError(c, workspace.ErrInvalidType)
		return
	}
	actor := auth.GetActorFromContext(c)
	allowed := isGlobalAdmin(actor)
	if req.Type == workspace.TypeDeployment {
		if _, err := s.manager.GetDeployment(req.ID); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "deployment not found"})
			return
		}
		allowed = allowed || (actor != nil && actor.CanAccessDeployment(req.ID, auth.AccessLevelAdmin))
	}
	if !allowed {
		c.JSON(http.StatusForbidden, gin.H{"error": "you must administer this resource to move it"})
		return
	}
	if err := s.workspaces().Assign(w.ID, req); err != nil {
		workspaceError(c, err)
		return
	}
	if req.Type == workspace.TypeDeployment {
		s.recordDeploymentWorkspace(req.ID, w.Slug)
	}
	c.JSON(http.StatusOK, gin.H{"workspace": w.Slug, "type": req.Type, "id": req.ID})
}
