package api

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/flatrun/agent/internal/ai"
	"github.com/gin-gonic/gin"
)

func (s *Server) getAIStatus(c *gin.Context) {
	resp := gin.H{"enabled": s.aiProvider != nil}
	if s.aiProvider != nil {
		resp["model"] = s.config.AI.Model
		if u, err := url.Parse(s.config.AI.BaseURL); err == nil {
			resp["base_url_host"] = u.Host
		}
	}
	c.JSON(http.StatusOK, resp)
}

// deploymentSecretValues collects every secret value that must never
// reach a model provider: the deployment's env values plus the agent's
// own credentials.
func (s *Server) deploymentSecretValues(name string) []string {
	var secrets []string
	envPath := filepath.Join(s.config.DeploymentsPath, name, ".env.flatrun")
	if content, err := os.ReadFile(envPath); err == nil {
		for _, v := range parseEnvContent(string(content)) {
			secrets = append(secrets, v.Value)
		}
	}
	secrets = append(secrets,
		s.config.AI.APIKey,
		s.config.Auth.JWTSecret,
		s.config.Infrastructure.Database.RootPassword,
		s.config.Infrastructure.Redis.Password,
		s.config.Infrastructure.PowerDNS.APIKey,
	)
	return secrets
}

func (s *Server) aiAnalyzeDeployment(c *gin.Context) {
	if s.aiProvider == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI assistant is not enabled", "code": "ai_disabled"})
		return
	}
	name := c.Param("name")

	var req struct {
		Mode            string `json:"mode"`
		Tail            int    `json:"tail"`
		Operation       string `json:"operation"`
		OperationOutput string `json:"operation_output"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Mode == "" {
		req.Mode = "logs"
	}

	if _, err := s.manager.GetDeployment(name); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}

	composeContent, _, err := s.manager.GetComposeFile(name)
	if err != nil {
		composeContent = ""
	}

	var contextLabel, contextBody string
	switch req.Mode {
	case "logs":
		tail := req.Tail
		if tail <= 0 {
			tail = 300
		}
		if tail > 1000 {
			tail = 1000
		}
		logs, lerr := s.manager.GetDeploymentLogs(name, tail)
		if lerr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read logs: " + lerr.Error()})
			return
		}
		contextLabel = "Recent logs"
		contextBody = logs
	case "operation":
		if strings.TrimSpace(req.OperationOutput) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": `operation_output is required for mode "operation"`})
			return
		}
		contextLabel = "Failed operation output"
		if req.Operation != "" {
			contextLabel = `Output of failed "` + req.Operation + `" operation`
		}
		contextBody = req.OperationOutput
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": `mode must be "logs" or "operation"`})
		return
	}

	redactor := ai.NewRedactor(s.deploymentSecretValues(name))
	redactedCompose, composeRedactions := redactor.Redact(composeContent)
	redactedContext, contextRedactions := redactor.Redact(contextBody)

	messages := ai.BuildDiagnosisMessages(name, redactedCompose, contextLabel, redactedContext)
	resp, err := s.aiProvider.Complete(c.Request.Context(), ai.Request{Messages: messages})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"analysis":   resp.Content,
		"model":      resp.Model,
		"redactions": composeRedactions + contextRedactions,
	})
}
