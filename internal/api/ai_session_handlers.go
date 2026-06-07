package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/flatrun/agent/internal/ai"
	"github.com/flatrun/agent/internal/auth"
	"github.com/gin-gonic/gin"
)

func sessionActorFrom(c *gin.Context) ai.SessionActor {
	actor := auth.GetActorFromContext(c)
	if actor == nil {
		return ai.SessionActor{ID: "anonymous", Name: "anonymous"}
	}
	a := ai.SessionActor{}
	switch {
	case actor.User != nil:
		a.ID = fmt.Sprintf("%d", actor.User.ID)
		a.Name = actor.User.Username
	case actor.APIKey != nil:
		a.ID = actor.APIKey.KeyID
		a.Name = actor.APIKey.Name
	}
	if a.ID == "" {
		a.ID = "anonymous"
	}
	return a
}

// canUseSession restricts a session to its creator (or an admin),
// since the transcript may reference resources only they can see.
func canUseSession(c *gin.Context, sess *ai.Session) bool {
	actor := auth.GetActorFromContext(c)
	if actor == nil || actor.Role == auth.RoleAdmin {
		return true
	}
	return sessionActorFrom(c).ID == sess.CreatedBy.ID
}

// advanceSession runs the tool loop: it calls the model, executes any
// requested tools (auto-run) or pauses for approval, and repeats until
// the model returns a final answer or the step budget is exhausted.
func (s *Server) advanceSession(c *gin.Context, sess *ai.Session) error {
	tools := s.aiToolSpecs()
	for step := 0; step < sess.MaxToolSteps(); step++ {
		resp, err := s.aiProvider.Complete(c.Request.Context(), ai.Request{Messages: sess.Messages, Tools: tools})
		if err != nil {
			return err
		}
		if sess.Model == "" {
			sess.Model = resp.Model
		}

		if len(resp.ToolCalls) == 0 {
			analysis, suggestions := ai.ParseSuggestions(resp.Content)
			sess.AddAssistantMessage(analysis, nil)
			sess.Suggested = s.scopeSuggestions(sess, suggestions)
			sess.Status = ai.SessionStatusReady
			return nil
		}

		sess.AddAssistantMessage(resp.Content, resp.ToolCalls)

		if !sess.AutoRun {
			sess.Pending = resp.ToolCalls
			sess.Status = ai.SessionStatusAwaitingApproval
			return nil
		}

		for _, call := range resp.ToolCalls {
			sess.AddToolResult(call, s.runAITool(c, sess.Deployment, call))
		}
	}

	sess.AddAssistantMessage("I stopped after investigating several steps without reaching a confident answer. Ask a more specific question or check the details directly.", nil)
	sess.Status = ai.SessionStatusReady
	return nil
}

func (s *Server) scopeSuggestions(sess *ai.Session, suggestions []ai.SuggestedAction) []ai.SuggestedAction {
	if len(suggestions) == 0 {
		return []ai.SuggestedAction{}
	}
	if sess.Scope == ai.SessionScopeDeployment && sess.Deployment != "" {
		return s.filterSuggestionsForDeployment(sess.Deployment, suggestions)
	}
	// System-scope suggestions have no single deployment to validate
	// against, so they are not offered as one-click actions.
	return []ai.SuggestedAction{}
}

func (s *Server) sessionResponse(c *gin.Context, sess *ai.Session) {
	c.JSON(http.StatusOK, gin.H{
		"id":                sess.ID,
		"scope":             sess.Scope,
		"deployment":        sess.Deployment,
		"auto_run":          sess.AutoRun,
		"status":            sess.Status,
		"model":             sess.Model,
		"messages":          sess.DisplayMessages(),
		"pending":           sess.Pending,
		"suggested_actions": sess.Suggested,
	})
}

func (s *Server) aiRequireEnabled(c *gin.Context) bool {
	if s.aiProvider == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI assistant is not enabled", "code": "ai_disabled"})
		return false
	}
	return true
}

func (s *Server) createAISession(c *gin.Context) {
	if !s.aiRequireEnabled(c) {
		return
	}
	var req struct {
		Scope      string `json:"scope"`
		Deployment string `json:"deployment"`
		AutoRun    bool   `json:"auto_run"`
		Message    string `json:"message"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Scope == "" {
		req.Scope = ai.SessionScopeSystem
	}
	if req.Scope == ai.SessionScopeDeployment {
		if req.Deployment == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "deployment is required for a deployment-scoped session"})
			return
		}
		if !s.requireDeploymentAccess(c, req.Deployment, auth.AccessLevelRead) {
			return
		}
	}
	if strings.TrimSpace(req.Message) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message is required"})
		return
	}

	prompt := ai.BuildSessionPrompt(req.Scope, req.Deployment, s.config.AI.DocsURL)
	sess := ai.NewSession(req.Scope, req.Deployment, req.AutoRun, sessionActorFrom(c), prompt)
	sess.AddUserMessage(req.Message)

	if err := s.advanceSession(c, sess); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if err := s.aiSessions.Save(sess); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	s.sessionResponse(c, sess)
}

func (s *Server) loadOwnedSession(c *gin.Context) (*ai.Session, bool) {
	sess, err := s.aiSessions.Get(c.Param("id"))
	if err == ai.ErrSessionNotFound {
		c.JSON(http.StatusNotFound, gin.H{"error": "Session not found"})
		return nil, false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return nil, false
	}
	if !canUseSession(c, sess) {
		c.JSON(http.StatusForbidden, gin.H{"error": "No access to this session"})
		return nil, false
	}
	return sess, true
}

func (s *Server) getAISession(c *gin.Context) {
	sess, ok := s.loadOwnedSession(c)
	if !ok {
		return
	}
	s.sessionResponse(c, sess)
}

func (s *Server) postAISessionMessage(c *gin.Context) {
	if !s.aiRequireEnabled(c) {
		return
	}
	sess, ok := s.loadOwnedSession(c)
	if !ok {
		return
	}
	if sess.Status == ai.SessionStatusAwaitingApproval {
		c.JSON(http.StatusConflict, gin.H{"error": "session is waiting for tool approval; resolve it first"})
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Message) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message is required"})
		return
	}
	if sess.Scope == ai.SessionScopeDeployment && !s.requireDeploymentAccess(c, sess.Deployment, auth.AccessLevelRead) {
		return
	}

	sess.AddUserMessage(req.Message)
	if err := s.advanceSession(c, sess); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if err := s.aiSessions.Save(sess); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	s.sessionResponse(c, sess)
}

func (s *Server) approveAISessionTools(c *gin.Context) {
	if !s.aiRequireEnabled(c) {
		return
	}
	sess, ok := s.loadOwnedSession(c)
	if !ok {
		return
	}
	if sess.Status != ai.SessionStatusAwaitingApproval {
		c.JSON(http.StatusConflict, gin.H{"error": "session has no tools awaiting approval"})
		return
	}
	var req struct {
		// Approved maps tool call id -> whether to run it. Missing or
		// false means declined.
		Approved map[string]bool `json:"approved"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if sess.Scope == ai.SessionScopeDeployment && !s.requireDeploymentAccess(c, sess.Deployment, auth.AccessLevelRead) {
		return
	}

	for _, call := range sess.Pending {
		if req.Approved[call.ID] {
			sess.AddToolResult(call, s.runAITool(c, sess.Deployment, call))
		} else {
			sess.AddToolResult(call, "The operator declined to run this command.")
		}
	}
	sess.Pending = nil
	sess.Status = ai.SessionStatusReady

	if err := s.advanceSession(c, sess); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if err := s.aiSessions.Save(sess); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	s.sessionResponse(c, sess)
}

func (s *Server) deleteAISession(c *gin.Context) {
	sess, ok := s.loadOwnedSession(c)
	if !ok {
		return
	}
	if err := s.aiSessions.Delete(sess.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Session deleted", "id": sess.ID})
}
