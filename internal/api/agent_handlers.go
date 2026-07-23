package api

import (
	"fmt"
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
	c.JSON(http.StatusOK, gin.H{"agent": agent})
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
