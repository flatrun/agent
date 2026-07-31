package api

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/flatrun/agent/internal/ai"
	"github.com/flatrun/agent/internal/auth"
	"github.com/gin-gonic/gin"
)

// listAgents returns every agent defined as a markdown file under the agents
// directory.
func (s *Server) listAgents(c *gin.Context) {
	agents, err := s.aiAgents.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"agents": agents, "dir": s.aiAgents.Dir()})
}

// getAgent returns one agent's parsed metadata and raw file content, for the
// panel's editor.
func (s *Server) getAgent(c *gin.Context) {
	name := c.Param("name")
	agent, err := s.aiAgents.Get(name)
	if err == ai.ErrAgentNotFound {
		c.JSON(http.StatusNotFound, gin.H{"error": "Agent not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	content, err := s.aiAgents.Raw(name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"agent": agent, "content": content})
}

// putAgent validates and writes an agent definition from the panel's editor.
func (s *Server) putAgent(c *gin.Context) {
	var req struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	agent, err := s.aiAgents.Write(c.Param("name"), req.Content)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	s.syncAgentSchedule(agent)
	c.JSON(http.StatusOK, gin.H{"agent": agent})
}

// syncAgentSchedule keeps the scheduler in step with an agent's file: a schedule
// registers (or updates) a task, and no schedule removes any task the agent had.
// A sync failure (e.g. an invalid cron) is logged, not fatal, so the definition
// still saves.
func (s *Server) syncAgentSchedule(agent *ai.Agent) {
	if s.schedulerManager == nil {
		return
	}
	if agent.Schedule == "" {
		if err := s.schedulerManager.RemoveAgentTask(agent.Name); err != nil {
			log.Printf("scheduler: failed to remove agent task for %s: %v", agent.Name, err)
		}
		return
	}
	if err := s.schedulerManager.SyncAgentTask(agent.Name, agent.Schedule, agent.Deployment); err != nil {
		log.Printf("scheduler: failed to schedule agent %s (%q): %v", agent.Name, agent.Schedule, err)
	}
}

// deleteAgent removes an agent definition.
func (s *Server) deleteAgent(c *gin.Context) {
	if err := s.aiAgents.Delete(c.Param("name")); err != nil {
		if err == ai.ErrAgentNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "Agent not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if s.schedulerManager != nil {
		if err := s.schedulerManager.RemoveAgentTask(c.Param("name")); err != nil {
			log.Printf("scheduler: failed to remove agent task for %s: %v", c.Param("name"), err)
		}
	}
	c.JSON(http.StatusOK, gin.H{"message": "Agent deleted", "name": c.Param("name")})
}

// runAgent executes an agent by seeding an AI session with its instructions.
// The run inherits every session guarantee the runtime provides:
// permission-gated tools, protected mode, secret redaction, and the pause for
// per-call approval before any state-changing tool.
func (s *Server) runAgent(c *gin.Context) {
	if !s.aiRequireEnabled(c) {
		return
	}
	agent, err := s.aiAgents.Get(c.Param("name"))
	if err == ai.ErrAgentNotFound {
		c.JSON(http.StatusNotFound, gin.H{"error": "Agent not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if agent.Scope == ai.SessionScopeDeployment && !s.requireDeploymentAccess(c, agent.Deployment, auth.AccessLevelRead) {
		return
	}

	var req struct {
		DryRun bool `json:"dry_run"`
	}
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	prompt := ai.BuildSessionPrompt(agent.Scope, agent.Deployment, s.config.AI.DocsURL)
	sess := ai.NewSession(agent.Scope, agent.Deployment, true, sessionActorFrom(c), prompt)
	sess.Agent = agent.Name
	sess.MaxSteps = agent.MaxSteps
	sess.DryRun = req.DryRun
	display := fmt.Sprintf("Run the %q agent", agent.Name)
	sess.AddUserMessage(s.redactSessionInput(sess, agent.Instructions), display, false)

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

// runAgentHeadless runs an agent without a human present, for the scheduler. It
// executes under an actor carrying only the agent's granted permissions, and
// auto-approves tools: the grant, not a person, is the gate, so a tool the grant
// does not cover fails inside the tool. A no-grant agent is read-only. The run is
// saved as a session, which is what the agent's run history reads.
func (s *Server) runAgentHeadless(ctx context.Context, agentName string) (string, error) {
	if s.aiProvider == nil {
		return "", fmt.Errorf("AI assistant is not enabled")
	}
	agent, err := s.aiAgents.Get(agentName)
	if err != nil {
		return "", err
	}

	actor := agentActor(agent)
	gc := toolGinContext(ctx, actor)

	prompt := ai.BuildSessionPrompt(agent.Scope, agent.Deployment, s.config.AI.DocsURL)
	sess := ai.NewSession(agent.Scope, agent.Deployment, true, ai.SessionActor{ID: "agent:" + agent.Name, Name: agent.Name}, prompt)
	sess.Agent = agent.Name
	sess.AddUserMessage(s.redactSessionInput(sess, agent.Instructions), fmt.Sprintf("Scheduled run of %q", agent.Name), false)

	if err := s.advanceSessionWith(gc, sess, true); err != nil {
		return "", err
	}
	if err := s.aiSessions.Save(sess); err != nil {
		return "", err
	}
	return fmt.Sprintf("agent %q run recorded as session %s (%s)", agent.Name, sess.ID, sess.Status), nil
}

// agentActor builds the actor a scheduled run executes under. The base is
// read-only (viewer); the agent's declared permissions add whatever writes it is
// trusted with, and a deployment-scoped agent is granted access to only its own
// deployment, at the level its grant implies.
func agentActor(agent *ai.Agent) *auth.ActorContext {
	actor := &auth.ActorContext{
		Type:        "agent",
		Role:        auth.RoleViewer,
		Permissions: append([]string(nil), agent.Permissions...),
	}
	if agent.Scope == ai.SessionScopeDeployment && agent.Deployment != "" {
		level := auth.AccessLevelRead
		for _, p := range agent.Permissions {
			if p == string(auth.PermDeploymentsWrite) {
				level = auth.AccessLevelWrite
			}
		}
		actor.Deployments = map[string]string{agent.Deployment: level}
	}
	return actor
}
