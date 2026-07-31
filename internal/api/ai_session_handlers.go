package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/flatrun/agent/internal/ai"
	"github.com/flatrun/agent/internal/auth"
	"github.com/gin-gonic/gin"
	"github.com/whilesmartgo/agents"
)

// redactSessionInput strips known secret values from operator-supplied text
// before it enters the transcript, so seeded file content or pasted output
// never carries a credential to the model provider.
func (s *Server) redactSessionInput(sess *ai.Session, text string) string {
	secrets := s.systemSecretValues()
	if sess.Deployment != "" {
		secrets = s.deploymentSecretValues(sess.Deployment)
	}
	redacted, _ := ai.NewRedactor(secrets).Redact(text)
	return redacted
}

// composeUserMessage merges a short message with optional bulky
// context. The model sees both; the operator's transcript shows only
// the message.
func composeUserMessage(message, context string) (content, display string) {
	message = strings.TrimSpace(message)
	context = strings.TrimSpace(context)
	if context == "" {
		return message, ""
	}
	return message + "\n\n" + context, message
}

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

const aiStepLimitMessage = "I stopped after investigating several steps without reaching a confident answer. Ask a more specific question or check the details directly."

// advanceSession drives the assistant's tool loop through the shared agents
// runner: it calls the model, runs any requested tools (auto-run) or pauses for
// approval, and repeats until a final answer or the step budget is spent.
func (s *Server) advanceSession(c *gin.Context, sess *ai.Session) error {
	return s.advanceSessionWith(c, sess, false)
}

// advanceSessionWith drives a session turn. autoApprove runs every requested
// tool without pausing: it is for headless (scheduled) runs, where the actor's
// permission grant, not a human, is the gate. Interactive turns pass false.
func (s *Server) advanceSessionWith(c *gin.Context, sess *ai.Session, autoApprove bool) error {
	engine := ai.NewCapturingEngine(s.aiProvider)
	runner := s.aiRunner(c, sess, engine, autoApprove)
	conv := &agents.Conversation{Messages: ai.MessagesToAgents(sess.Messages)}
	from := len(conv.Messages)
	_, err := runner.Advance(c.Request.Context(), conv)
	return s.absorbAdvance(sess, conv, from, engine, err)
}

// aiRunner assembles the runner for one session turn. A session that does not
// auto-run pauses before any tools run, surfacing them for per-call approval.
// Even with auto-run on, a batch containing a state-changing tool pauses:
// reads run free, writes always ask.
func (s *Server) aiRunner(c *gin.Context, sess *ai.Session, engine agents.Engine, autoApprove bool) agents.Runner {
	runner := agents.Runner{
		Engine: engine,
		Harness: agents.Harness{
			Model:            sess.Model,
			MaxSteps:         sess.MaxToolSteps(),
			Tools:            s.sessionToolRegistry(c, sess.Deployment),
			StepLimitMessage: aiStepLimitMessage,
		},
	}
	runner.Approve = func(_ context.Context, calls []agents.ToolCall) (bool, error) {
		// A headless run has no human to ask: approve every call and let the
		// actor's permission grant deny anything it does not cover, inside the
		// tool. Interactive runs still pause before a state-changing tool.
		if autoApprove {
			return true, nil
		}
		if !sess.AutoRun {
			return false, nil
		}
		for _, call := range calls {
			if s.toolMutates(call.Name) {
				return false, nil
			}
		}
		return true, nil
	}
	return runner
}

// sessionToolRegistry exposes the assistant's tools to the runner, each bound to
// this request and the session's deployment so per-tool permission and
// protected-mode checks run exactly as they do for a direct tool call.
func (s *Server) sessionToolRegistry(c *gin.Context, deployment string) *agents.Registry {
	specs := s.aiToolSpecs()
	tools := make([]agents.Tool, 0, len(specs))
	for _, spec := range specs {
		spec := spec
		tools = append(tools, agents.Tool{
			Name:        spec.Name,
			Description: spec.Description,
			Parameters:  spec.Parameters,
			Handler: func(_ context.Context, raw json.RawMessage) (string, error) {
				return s.runAITool(c, deployment, ai.ToolCall{Name: spec.Name, Arguments: string(raw)}), nil
			},
		})
	}
	return agents.NewRegistry(tools...)
}

// absorbAdvance folds the messages the runner appended back into the stored
// session, records the model, and sets the resulting status. A paused turn
// records its pending calls; a real engine error is returned so the caller
// leaves the session unsaved.
func (s *Server) absorbAdvance(sess *ai.Session, conv *agents.Conversation, from int, engine *ai.CapturingEngine, err error) error {
	if err != nil && !errors.Is(err, agents.ErrAwaitingApproval) {
		return err
	}

	added := conv.Messages[from:]
	final := err == nil
	for i, m := range added {
		switch m.Role {
		case agents.RoleAssistant:
			if i == len(added)-1 && final {
				// The last assistant turn of a completed run is the answer; its
				// suggestion block is parsed out and offered as one-click actions.
				analysis, suggestions := ai.ParseSuggestions(m.Content)
				sess.AddAssistantMessage(analysis, ai.ToolCallsFromAgents(m.ToolCalls))
				sess.Suggested = s.scopeSuggestions(sess, suggestions)
			} else {
				sess.AddAssistantMessage(m.Content, ai.ToolCallsFromAgents(m.ToolCalls))
			}
		case agents.RoleTool:
			sess.AddToolResult(ai.ToolCall{ID: m.ToolCallID, Name: m.Name}, m.Content)
		}
	}
	if sess.Model == "" {
		sess.Model = engine.LastModel()
	}

	if errors.Is(err, agents.ErrAwaitingApproval) {
		last := conv.Messages[len(conv.Messages)-1]
		sess.Pending = ai.ToolCallsFromAgents(last.ToolCalls)
		sess.Status = ai.SessionStatusAwaitingApproval
		return nil
	}
	sess.Pending = nil
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
		"agent":             sess.Agent,
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
		Context    string `json:"context"`
		Seed       bool   `json:"seed"`
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
	content, display := composeUserMessage(req.Message, req.Context)
	sess.AddUserMessage(s.redactSessionInput(sess, content), display, req.Seed)

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

// listAISessions returns the caller's saved sessions (all sessions for an admin),
// most recent first, so past conversations can be resumed. An optional agent
// query parameter narrows the list to one agent's runs, which is how a run
// history is read from an agent's definition.
func (s *Server) listAISessions(c *gin.Context) {
	summaries, err := s.aiSessions.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	actor := auth.GetActorFromContext(c)
	isAdmin := actor != nil && actor.Role == auth.RoleAdmin
	mine := sessionActorFrom(c).ID
	agentFilter := c.Query("agent")
	out := make([]ai.SessionSummary, 0, len(summaries))
	for _, sum := range summaries {
		if !isAdmin && sum.CreatedBy.ID != mine {
			continue
		}
		if agentFilter != "" && sum.Agent != agentFilter {
			continue
		}
		out = append(out, sum)
	}
	c.JSON(http.StatusOK, gin.H{"sessions": out})
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
		Context string `json:"context"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Message) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message is required"})
		return
	}
	if sess.Scope == ai.SessionScopeDeployment && !s.requireDeploymentAccess(c, sess.Deployment, auth.AccessLevelRead) {
		return
	}

	content, display := composeUserMessage(req.Message, req.Context)
	sess.AddUserMessage(s.redactSessionInput(sess, content), display, false)
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

	// A missing or false decision means declined, so a nil map must not be read
	// as "approve all"; an empty non-nil map declines every pending call.
	decisions := req.Approved
	if decisions == nil {
		decisions = map[string]bool{}
	}
	engine := ai.NewCapturingEngine(s.aiProvider)
	runner := s.aiRunner(c, sess, engine, false)
	conv := &agents.Conversation{Messages: ai.MessagesToAgents(sess.Messages)}
	from := len(conv.Messages)
	_, err := runner.Resume(c.Request.Context(), conv, decisions)
	if err := s.absorbAdvance(sess, conv, from, engine, err); err != nil {
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
