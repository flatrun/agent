package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flatrun/agent/internal/audit"
	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/envref"
	"github.com/flatrun/agent/internal/secrets"
	"github.com/flatrun/agent/internal/workspace"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const secretFetchTimeout = 30 * time.Second

var connectionKinds = map[string]models.CredentialKind{
	"infisical":   models.CredentialKindInfisical,
	"vault":       models.CredentialKindVault,
	"doppler":     models.CredentialKindDoppler,
	"onepassword": models.CredentialKindOnePassword,
}

func (s *Server) registerSecretRoutes(r *gin.RouterGroup) {
	read := s.authMiddleware.RequirePermission(auth.PermSecretsRead)
	write := s.authMiddleware.RequirePermission(auth.PermSecretsWrite)

	r.GET("/secrets", read, s.listSecrets)
	r.PUT("/secrets/:secret", write, s.setSecret)
	r.DELETE("/secrets/:secret", write, s.deleteSecret)
	r.POST("/secrets/:secret/rotate", write, s.rotateSecret)
	r.PUT("/secrets/:secret/rotation", write, s.setSecretRotation)
	r.DELETE("/secrets/:secret/rotation", write, s.clearSecretRotation)

	r.GET("/secret-providers", read, s.listSecretProviders)
	r.GET("/secret-connections", read, s.listSecretConnections)
	r.POST("/secret-connections", write, s.createSecretConnection)
	r.PUT("/secret-connections/:id", write, s.updateSecretConnection)
	r.DELETE("/secret-connections/:id", write, s.deleteSecretConnection)

	r.GET("/secret-bindings", read, s.listSecretBindings)
	r.POST("/secret-bindings", write, s.createSecretBinding)
	r.DELETE("/secret-bindings/:id", write, s.deleteSecretBinding)
	r.POST("/secret-bindings/:id/sync", write, s.syncSecretBinding)
}

func (s *Server) secretsReady(c *gin.Context) bool {
	if s.secretStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "the secret store is not available"})
		return false
	}
	return true
}

// authorizeScope checks the actor may read, or with write change, a scope.
// Workspace scopes follow workspace roles; deployment scopes follow
// deployment access.
func (s *Server) authorizeScope(c *gin.Context, scope secrets.Scope, write bool) bool {
	switch scope.Type {
	case secrets.ScopeWorkspace:
		if s.workspaces() == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "workspace secrets need authentication to be enabled"})
			return false
		}
		w, err := s.workspaces().Get(scope.ID)
		if err != nil {
			workspaceError(c, err)
			return false
		}
		role, err := s.workspaceRole(auth.GetActorFromContext(c), w)
		if err != nil {
			workspaceError(c, err)
			return false
		}
		if role == "" {
			workspaceError(c, workspace.ErrNotFound)
			return false
		}
		if write && workspace.RoleRank(role) < workspace.RoleRank(workspace.RoleAdmin) {
			c.JSON(http.StatusForbidden, gin.H{"error": "workspace secrets are changed by workspace admins"})
			return false
		}
		return true
	case secrets.ScopeDeployment:
		if _, err := s.manager.GetDeployment(scope.ID); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "deployment not found"})
			return false
		}
		level := auth.AccessLevelRead
		if write {
			level = auth.AccessLevelWrite
		}
		return s.requireDeploymentAccess(c, scope.ID, level)
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": "secrets can be scoped to a workspace or a deployment"})
	return false
}

func (s *Server) requestScope(c *gin.Context, write bool) (secrets.Scope, bool) {
	scope, err := secrets.ParseScope(c.Query("scope"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return scope, false
	}
	return scope, s.authorizeScope(c, scope, write)
}

func secretError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, secrets.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, secrets.ErrInvalidName):
		status = http.StatusBadRequest
	}
	c.JSON(status, gin.H{"error": err.Error()})
}

// secretChain is the scopes whose secrets a deployment can read, broadest
// first.
func (s *Server) secretChain(deployment string) []secrets.Scope {
	var chain []secrets.Scope
	if store := s.workspaces(); store != nil {
		if owner, err := store.Owner(workspace.Ref{Type: workspace.TypeDeployment, ID: deployment}); err == nil {
			chain = append(chain, secrets.Scope{Type: secrets.ScopeWorkspace, ID: owner.Slug})
		}
	}
	return append(chain, secrets.Scope{Type: secrets.ScopeDeployment, ID: deployment})
}

func (s *Server) secretResolver() secrets.Resolver {
	return secrets.Resolver{
		Store:    s.secretStore,
		Registry: s.secretRegistry,
		Connections: func(id string) (string, map[string]string, error) {
			if s.credentialsManager == nil {
				return "", nil, errors.New("credentials are not available")
			}
			cred, err := s.credentialsManager.GetGenericCredential(id)
			if err != nil {
				return "", nil, err
			}
			return string(cred.Kind), cred.Data, nil
		},
	}
}

// resolvedSecretsFor is what ${{ secret.NAME }} reads for a deployment.
func (s *Server) resolvedSecretsFor(deployment string) (map[string]string, error) {
	if s.secretStore == nil {
		return nil, errors.New("the secret store is not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), secretFetchTimeout)
	defer cancel()
	return s.secretResolver().Values(ctx, s.secretChain(deployment))
}

// scopedEnvSource reads other deployments' variables like deploymentEnvSource
// and resolves the secret target for the deployment being written.
type scopedEnvSource struct {
	deploymentEnvSource
	self string
}

func (e scopedEnvSource) Raw(target string) (map[string]string, error) {
	if target == secrets.ReferenceName {
		return e.s.resolvedSecretsFor(e.self)
	}
	return e.deploymentEnvSource.Raw(target)
}

func (e scopedEnvSource) Host(target string) (string, error) {
	if target == secrets.ReferenceName {
		return "", envref.ErrUnknownDeployment
	}
	return e.deploymentEnvSource.Host(target)
}

// secretUsers lists the deployments in reach of a scope whose variables
// reference any of names.
func (s *Server) secretUsers(scope secrets.Scope, names ...string) []string {
	var candidates []string
	switch scope.Type {
	case secrets.ScopeDeployment:
		candidates = []string{scope.ID}
	case secrets.ScopeWorkspace:
		if store := s.workspaces(); store != nil {
			if w, err := store.Get(scope.ID); err == nil {
				candidates, _ = store.Resources(w.ID, workspace.TypeDeployment)
			}
		}
	}
	var users []string
	for _, dep := range candidates {
		refs, err := s.readEnvReferences(dep)
		if err != nil {
			continue
		}
		for _, value := range refs {
			if referencesSecret(value, names) {
				users = append(users, dep)
				break
			}
		}
	}
	sort.Strings(users)
	return users
}

func referencesSecret(value string, names []string) bool {
	compact := strings.ReplaceAll(value, " ", "")
	for _, name := range names {
		if name != "" && strings.Contains(compact, "${{"+secrets.ReferenceName+"."+name+"}}") {
			return true
		}
	}
	return false
}

// applySecretChange rewrites the env files of the deployments that use the
// changed secrets. With restart, running ones are restarted so containers
// see the new values.
func (s *Server) applySecretChange(scope secrets.Scope, restart bool, names ...string) (refreshed, restarted []string, failed map[string]string) {
	failed = map[string]string{}
	for _, dep := range s.secretUsers(scope, names...) {
		if err := s.refreshEnvReferences(dep); err != nil {
			failed[dep] = err.Error()
			continue
		}
		refreshed = append(refreshed, dep)
		if !restart || s.jobs == nil {
			continue
		}
		if d, err := s.manager.GetDeployment(dep); err == nil && d.Status == "running" {
			if _, created := s.startDeploymentJob(dep, "restart", actionOptions{}); created {
				restarted = append(restarted, dep)
			}
		}
	}
	return refreshed, restarted, failed
}

func (s *Server) listSecrets(c *gin.Context) {
	if !s.secretsReady(c) {
		return
	}
	scope, ok := s.requestScope(c, false)
	if !ok {
		return
	}
	list, err := s.secretStore.List(scope)
	if err != nil {
		secretError(c, err)
		return
	}
	c.JSON(http.StatusOK, NewList(list, ""))
}

type secretValueRequest struct {
	Value    *string            `json:"value"`
	Generate *secrets.Generator `json:"generate"`
}

func (s *Server) setSecret(c *gin.Context) {
	if !s.secretsReady(c) {
		return
	}
	scope, ok := s.requestScope(c, true)
	if !ok {
		return
	}
	var req secretValueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var value string
	switch {
	case req.Value != nil && req.Generate == nil:
		value = *req.Value
	case req.Generate != nil && req.Value == nil:
		generated, err := req.Generate.Generate()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		value = generated
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "send either a value or generate"})
		return
	}
	name := c.Param("secret")
	sec, err := s.secretStore.Set(scope, name, value)
	if err != nil {
		secretError(c, err)
		return
	}
	refreshed, _, failed := s.applySecretChange(scope, false, name)
	c.JSON(http.StatusOK, gin.H{"secret": sec, "refreshed": nonNil(refreshed), "failed": failed})
}

func (s *Server) deleteSecret(c *gin.Context) {
	if !s.secretsReady(c) {
		return
	}
	scope, ok := s.requestScope(c, true)
	if !ok {
		return
	}
	name := c.Param("secret")
	if users := s.secretUsers(scope, name); len(users) > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "the secret is still referenced", "deployments": users})
		return
	}
	if err := s.secretStore.Delete(scope, name); err != nil {
		secretError(c, err)
		return
	}
	if s.schedulerManager != nil {
		if err := s.schedulerManager.RemoveRotationTask(scope.String(), name); err != nil {
			log.Printf("Warning: failed to remove rotation of %s/%s: %v", scope, name, err)
		}
	}
	c.Status(http.StatusNoContent)
}

// rotateStoredSecret regenerates a secret, refreshes the deployments that use
// it, restarts the running ones and records the rotation.
func (s *Server) rotateStoredSecret(scope secrets.Scope, name, actor string) (*secrets.Secret, []string, []string, map[string]string, error) {
	sec, err := s.secretStore.Rotate(scope, name)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	refreshed, restarted, failed := s.applySecretChange(scope, true, name)
	if s.auditManager != nil {
		event := &audit.AuditEvent{
			EventID:      uuid.New().String(),
			Timestamp:    time.Now(),
			ActorType:    audit.ActorTypeSystem,
			ActorName:    actor,
			Action:       "secret.rotate",
			ResourceType: "secret",
			ResourceID:   scope.String() + "/" + name,
			Success:      len(failed) == 0,
			Metadata:     fmt.Sprintf(`{"version":%d,"restarted":%q}`, sec.Version, strings.Join(restarted, ",")),
		}
		if err := s.auditManager.LogEvent(event); err != nil {
			log.Printf("Warning: failed to audit rotation of %s/%s: %v", scope, name, err)
		}
	}
	return sec, refreshed, restarted, failed, nil
}

func (s *Server) rotateSecret(c *gin.Context) {
	if !s.secretsReady(c) {
		return
	}
	scope, ok := s.requestScope(c, true)
	if !ok {
		return
	}
	actorName := ""
	if actor := auth.GetActorFromContext(c); actor != nil && actor.User != nil {
		actorName = actor.User.Username
	}
	sec, refreshed, restarted, failed, err := s.rotateStoredSecret(scope, c.Param("secret"), actorName)
	if err != nil {
		secretError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"secret": sec, "refreshed": nonNil(refreshed), "restarted": nonNil(restarted), "failed": failed})
}

// runScheduledRotation is the scheduler's entry point for rotation tasks.
func (s *Server) runScheduledRotation(_ context.Context, scopeText, name string) (string, error) {
	if s.secretStore == nil {
		return "", errors.New("the secret store is not available")
	}
	scope, err := secrets.ParseScope(scopeText)
	if err != nil {
		return "", err
	}
	sec, _, restarted, failed, err := s.rotateStoredSecret(scope, name, "scheduler")
	if err != nil {
		return "", err
	}
	out := fmt.Sprintf("rotated %s/%s to version %d", scope, name, sec.Version)
	if len(restarted) > 0 {
		out += "; restarting " + strings.Join(restarted, ", ")
	}
	if len(failed) > 0 {
		return out, fmt.Errorf("could not refresh %d deployment(s): %v", len(failed), failed)
	}
	return out, nil
}

func (s *Server) setSecretRotation(c *gin.Context) {
	if !s.secretsReady(c) {
		return
	}
	scope, ok := s.requestScope(c, true)
	if !ok {
		return
	}
	if s.schedulerManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "the scheduler is not running"})
		return
	}
	var req secrets.Rotation
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.schedulerManager.ValidateCronExpr(req.Schedule); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid schedule: " + err.Error()})
		return
	}
	name := c.Param("secret")
	sec, err := s.secretStore.SetRotation(scope, name, &req)
	if err != nil {
		if errors.Is(err, secrets.ErrNotFound) {
			secretError(c, err)
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.schedulerManager.SyncRotationTask(scope.String(), name, req.Schedule, scope.Deployment()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, sec)
}

func (s *Server) clearSecretRotation(c *gin.Context) {
	if !s.secretsReady(c) {
		return
	}
	scope, ok := s.requestScope(c, true)
	if !ok {
		return
	}
	name := c.Param("secret")
	sec, err := s.secretStore.SetRotation(scope, name, nil)
	if err != nil {
		secretError(c, err)
		return
	}
	if s.schedulerManager != nil {
		if err := s.schedulerManager.RemoveRotationTask(scope.String(), name); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, sec)
}

type secretProviderView struct {
	Type             string          `json:"type"`
	Label            string          `json:"label"`
	ConnectionFields []secrets.Field `json:"connection_fields"`
	BindingFields    []secrets.Field `json:"binding_fields"`
}

func (s *Server) listSecretProviders(c *gin.Context) {
	if !s.secretsReady(c) {
		return
	}
	views := []secretProviderView{}
	for _, p := range s.secretRegistry.All() {
		views = append(views, secretProviderView{Type: p.Type(), Label: p.Label(), ConnectionFields: p.ConnectionFields(), BindingFields: p.BindingFields()})
	}
	c.JSON(http.StatusOK, NewList(views, ""))
}

type secretConnectionView struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Provider  string            `json:"provider"`
	Settings  map[string]string `json:"settings"`
	Workspace string            `json:"workspace,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

func connectionView(cred models.Credential, ws string) secretConnectionView {
	settings := make(map[string]string, len(cred.Data))
	for k, v := range cred.Data {
		if models.IsSecretKey(cred.Kind, k) && v != "" {
			v = models.CredentialMask
		}
		settings[k] = v
	}
	return secretConnectionView{ID: cred.ID, Name: cred.Name, Provider: string(cred.Kind), Settings: settings, Workspace: ws, CreatedAt: cred.CreatedAt, UpdatedAt: cred.UpdatedAt}
}

func isConnectionKind(kind models.CredentialKind) bool {
	for _, k := range connectionKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// connectionRole is the actor's workspace role over a connection, through
// the workspace that owns it.
func (s *Server) connectionRole(c *gin.Context, id string) (*models.Credential, string, *workspace.Workspace, bool) {
	if s.credentialsManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "credentials are not available"})
		return nil, "", nil, false
	}
	cred, err := s.credentialsManager.GetGenericCredential(id)
	if err != nil || !isConnectionKind(cred.Kind) {
		c.JSON(http.StatusNotFound, gin.H{"error": "connection not found"})
		return nil, "", nil, false
	}
	actor := auth.GetActorFromContext(c)
	if s.workspaces() == nil {
		return cred, workspace.RoleOwner, nil, true
	}
	owner, err := s.workspaces().Owner(workspace.Ref{Type: workspace.TypeCredential, ID: id})
	if err != nil {
		if isGlobalAdmin(actor) {
			return cred, workspace.RoleOwner, nil, true
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "connection not found"})
		return nil, "", nil, false
	}
	role, err := s.workspaceRole(actor, owner)
	if err != nil || role == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "connection not found"})
		return nil, "", nil, false
	}
	return cred, role, owner, true
}

func (s *Server) listSecretConnections(c *gin.Context) {
	if !s.secretsReady(c) || s.credentialsManager == nil {
		return
	}
	scope, ok := s.requestWorkspace(c)
	if !ok {
		return
	}
	actor := auth.GetActorFromContext(c)
	owners := map[string]string{}
	if store := s.workspaces(); store != nil {
		owners, _ = store.Owners(workspace.TypeCredential)
	}
	member := map[string]bool{}
	if store := s.workspaces(); store != nil && actor != nil && actor.User != nil {
		if memberships, err := store.ListForUser(actor.User.ID); err == nil {
			for _, m := range memberships {
				member[m.Slug] = true
			}
		}
	}
	views := []secretConnectionView{}
	for _, cred := range s.credentialsManager.ListGenericCredentials("") {
		if !isConnectionKind(cred.Kind) {
			continue
		}
		ws := owners[cred.ID]
		if scope != nil && ws != scope.Slug {
			continue
		}
		if s.workspaces() != nil && !isGlobalAdmin(actor) && !member[ws] {
			continue
		}
		views = append(views, connectionView(cred, ws))
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	c.JSON(http.StatusOK, NewList(views, ""))
}

func (s *Server) createSecretConnection(c *gin.Context) {
	if !s.secretsReady(c) {
		return
	}
	if s.credentialsManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "credentials are not available"})
		return
	}
	var req struct {
		Name     string            `json:"name" binding:"required"`
		Provider string            `json:"provider" binding:"required"`
		Settings map[string]string `json:"settings"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	provider, ok := s.secretRegistry.Get(req.Provider)
	kind, known := connectionKinds[req.Provider]
	if !ok || !known {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown secret provider " + req.Provider})
		return
	}
	if err := secrets.CheckFields(provider.ConnectionFields(), req.Settings); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ws, ok := s.creationWorkspace(c)
	if !ok {
		return
	}
	if ws != nil {
		role, err := s.workspaceRole(auth.GetActorFromContext(c), ws)
		if err != nil || workspace.RoleRank(role) < workspace.RoleRank(workspace.RoleAdmin) {
			c.JSON(http.StatusForbidden, gin.H{"error": "connections are added by workspace admins"})
			return
		}
	}
	cred, err := s.credentialsManager.CreateGenericCredential(req.Name, kind, req.Settings)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	slug := ""
	if ws != nil {
		if err := s.workspaces().Assign(ws.ID, workspace.Ref{Type: workspace.TypeCredential, ID: cred.ID}); err != nil {
			_ = s.credentialsManager.DeleteGenericCredential(cred.ID)
			workspaceError(c, err)
			return
		}
		slug = ws.Slug
	}
	c.JSON(http.StatusCreated, connectionView(*cred, slug))
}

func (s *Server) updateSecretConnection(c *gin.Context) {
	if !s.secretsReady(c) {
		return
	}
	cred, role, owner, ok := s.connectionRole(c, c.Param("id"))
	if !ok {
		return
	}
	if workspace.RoleRank(role) < workspace.RoleRank(workspace.RoleAdmin) {
		c.JSON(http.StatusForbidden, gin.H{"error": "connections are changed by workspace admins"})
		return
	}
	var req struct {
		Name     string            `json:"name"`
		Settings map[string]string `json:"settings"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updated, err := s.credentialsManager.UpdateGenericCredential(cred.ID, req.Name, req.Settings)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	slug := ""
	if owner != nil {
		slug = owner.Slug
	}
	c.JSON(http.StatusOK, connectionView(*updated, slug))
}

func (s *Server) deleteSecretConnection(c *gin.Context) {
	if !s.secretsReady(c) {
		return
	}
	cred, role, _, ok := s.connectionRole(c, c.Param("id"))
	if !ok {
		return
	}
	if workspace.RoleRank(role) < workspace.RoleRank(workspace.RoleAdmin) {
		c.JSON(http.StatusForbidden, gin.H{"error": "connections are removed by workspace admins"})
		return
	}
	inUse, err := s.secretStore.ConnectionInUse(cred.ID)
	if err != nil {
		secretError(c, err)
		return
	}
	if inUse {
		c.JSON(http.StatusConflict, gin.H{"error": "remove the bindings that use this connection first"})
		return
	}
	if err := s.credentialsManager.DeleteGenericCredential(cred.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if store := s.workspaces(); store != nil {
		_ = store.Release(workspace.Ref{Type: workspace.TypeCredential, ID: cred.ID})
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) listSecretBindings(c *gin.Context) {
	if !s.secretsReady(c) {
		return
	}
	scope, ok := s.requestScope(c, false)
	if !ok {
		return
	}
	bindings, err := s.secretStore.Bindings(scope)
	if err != nil {
		secretError(c, err)
		return
	}
	c.JSON(http.StatusOK, NewList(bindings, ""))
}

func (s *Server) createSecretBinding(c *gin.Context) {
	if !s.secretsReady(c) {
		return
	}
	scope, ok := s.requestScope(c, true)
	if !ok {
		return
	}
	var req struct {
		ConnectionID string            `json:"connection_id" binding:"required"`
		Options      map[string]string `json:"options"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	cred, role, _, ok := s.connectionRole(c, req.ConnectionID)
	if !ok {
		return
	}
	if workspace.RoleRank(role) < workspace.RoleRank(workspace.RoleAdmin) {
		c.JSON(http.StatusForbidden, gin.H{"error": "connections are bound by workspace admins"})
		return
	}
	provider, found := s.secretRegistry.Get(string(cred.Kind))
	if !found {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown secret provider " + string(cred.Kind)})
		return
	}
	if err := secrets.CheckFields(provider.BindingFields(), req.Options); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	binding := secrets.Binding{Scope: scope, Provider: provider.Type(), ConnectionID: cred.ID, Options: req.Options}
	ctx, cancel := context.WithTimeout(c.Request.Context(), secretFetchTimeout)
	defer cancel()
	keys, err := s.secretResolver().FetchBinding(ctx, binding)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not read from " + provider.Label() + ": " + err.Error()})
		return
	}
	created, err := s.secretStore.AddBinding(binding)
	if err != nil {
		secretError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"binding": created, "keys": sortedKeys(keys)})
}

func (s *Server) loadBinding(c *gin.Context) (*secrets.Binding, bool) {
	b, err := s.secretStore.Binding(c.Param("id"))
	if err != nil {
		secretError(c, err)
		return nil, false
	}
	if !s.authorizeScope(c, b.Scope, true) {
		return nil, false
	}
	return b, true
}

func (s *Server) deleteSecretBinding(c *gin.Context) {
	if !s.secretsReady(c) {
		return
	}
	b, ok := s.loadBinding(c)
	if !ok {
		return
	}
	if err := s.secretStore.DeleteBinding(b.ID); err != nil {
		secretError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// syncSecretBinding reads the bound manager again and rewrites the variables
// of every deployment in reach that uses a secret.
func (s *Server) syncSecretBinding(c *gin.Context) {
	if !s.secretsReady(c) {
		return
	}
	b, ok := s.loadBinding(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), secretFetchTimeout)
	defer cancel()
	keys, err := s.secretResolver().FetchBinding(ctx, *b)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	names := sortedKeys(keys)
	refreshed, _, failed := s.applySecretChange(b.Scope, false, names...)
	c.JSON(http.StatusOK, gin.H{"keys": names, "refreshed": nonNil(refreshed), "failed": failed})
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func openSecretStore(deploymentsPath string) (*secrets.Keyring, *secrets.Store, error) {
	dir := filepath.Join(deploymentsPath, ".flatrun")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, nil, err
	}
	keyring, err := secrets.LoadKeyring(filepath.Join(dir, "secrets.key"))
	if err != nil {
		return nil, nil, err
	}
	return keyring, secrets.NewStore(filepath.Join(dir, "secrets.json"), keyring), nil
}
