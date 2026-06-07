package api

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/flatrun/agent/internal/ai"
	"github.com/gin-gonic/gin"
)

func (s *Server) getAIStatus(c *gin.Context) {
	resp := gin.H{"enabled": s.aiProvider != nil, "intents": ai.IntentKeys()}
	if s.aiProvider != nil {
		resp["model"] = s.config.AI.Model
		if u, err := url.Parse(s.config.AI.BaseURL); err == nil {
			resp["base_url_host"] = u.Host
		}
	}
	c.JSON(http.StatusOK, resp)
}

type assistSource struct {
	Type    string `json:"type"`
	Label   string `json:"label"`
	Content string `json:"content"`
	Tail    int    `json:"tail"`
}

type assistRequest struct {
	Intent   string         `json:"intent"`
	Sources  []assistSource `json:"sources"`
	Question string         `json:"question"`
}

// collectDeploymentSource gathers one labeled context section for a
// deployment scope. New source types (nginx logs, security events,
// linked codebases) register here without touching the pipeline.
func (s *Server) collectDeploymentSource(name string, src assistSource) (ai.Section, *apiError) {
	switch src.Type {
	case "logs":
		tail := src.Tail
		if tail <= 0 {
			tail = 300
		}
		if tail > 1000 {
			tail = 1000
		}
		logs, err := s.manager.GetDeploymentLogs(name, tail)
		if err != nil {
			return ai.Section{}, apiErrf(http.StatusInternalServerError, "Failed to read logs: %s", err.Error())
		}
		return ai.Section{Label: "Recent logs", Content: logs}, nil
	case "compose":
		content, filename, err := s.manager.GetComposeFile(name)
		if err != nil {
			return ai.Section{}, apiErrf(http.StatusNotFound, "Compose file not found")
		}
		return ai.Section{Label: filename, Content: content, Format: "yaml"}, nil
	case "provided":
		if strings.TrimSpace(src.Content) == "" {
			return ai.Section{}, apiErrf(http.StatusBadRequest, "provided source requires content")
		}
		label := src.Label
		if label == "" {
			label = "Provided output"
		}
		return ai.Section{Label: label, Content: src.Content}, nil
	default:
		return ai.Section{}, apiErrf(http.StatusBadRequest, "unknown source type %q", src.Type)
	}
}

// platformSection states this installation's live configuration so
// analyses are grounded in FlatRun specifics instead of generic Docker
// knowledge: which networks the platform expects, which networks
// actually exist, and how the deployment is wired into them.
func (s *Server) platformSection(deploymentName string) ai.Section {
	var b strings.Builder
	cfg := s.config

	fmt.Fprintf(&b, "Configured proxy network (apps must join this external network to be served on the web): %s\n", cfg.Infrastructure.DefaultProxyNetwork)
	fmt.Fprintf(&b, "Configured database network (apps reach shared databases over this external network): %s\n", cfg.Infrastructure.DefaultDatabaseNetwork)

	if networks, err := s.networksManager.ListNetworks(); err == nil {
		names := make([]string, 0, len(networks))
		for _, n := range networks {
			names = append(names, n.Name)
		}
		fmt.Fprintf(&b, "Docker networks that currently exist on this host: %s\n", strings.Join(names, ", "))
	}

	if cfg.Infrastructure.Database.Enabled {
		fmt.Fprintf(&b, "Shared %s database server is available at host %q on the database network\n",
			cfg.Infrastructure.Database.Type, cfg.Infrastructure.Database.Host)
	}
	fmt.Fprintf(&b, "Nginx reverse proxy managed by FlatRun: %t\n", cfg.Nginx.Enabled)

	if deploymentName != "" {
		if deployment, err := s.manager.GetDeployment(deploymentName); err == nil && deployment.Metadata != nil {
			if domains := deployment.Metadata.GetUniqueDomainNames(); len(domains) > 0 {
				fmt.Fprintf(&b, "This deployment serves the domain(s): %s\n", strings.Join(domains, ", "))
			}
			if len(deployment.Metadata.Databases) > 0 {
				aliases := make([]string, 0, len(deployment.Metadata.Databases))
				for _, db := range deployment.Metadata.Databases {
					aliases = append(aliases, fmt.Sprintf("%s (%s)", db.Alias, db.Type))
				}
				fmt.Fprintf(&b, "This deployment uses database(s): %s\n", strings.Join(aliases, ", "))
			}
		}
		if s.proxyOrchestrator != nil && s.proxyOrchestrator.NginxManager().VirtualHostExists(deploymentName) {
			fmt.Fprintf(&b, "A virtual host for this deployment exists in the reverse proxy\n")
		}
	}

	return ai.Section{Label: "FlatRun platform context", Content: b.String()}
}

func (s *Server) runAssist(c *gin.Context, scopeLabel string, sections []ai.Section, req assistRequest, secrets []string, validateSuggestions func([]ai.SuggestedAction) []ai.SuggestedAction) {
	intent, ok := ai.GetIntent(req.Intent)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown intent; valid intents: " + strings.Join(ai.IntentKeys(), ", ")})
		return
	}

	redactor := ai.NewRedactor(secrets)
	redactions := 0
	for i := range sections {
		redacted, n := redactor.Redact(sections[i].Content)
		sections[i].Content = redacted
		redactions += n
	}

	messages := ai.BuildAssistMessages(intent, scopeLabel, sections, req.Question, s.config.AI.DocsURL)
	resp, err := s.aiProvider.Complete(c.Request.Context(), ai.Request{Messages: messages})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	analysis, suggestions := ai.ParseSuggestions(resp.Content)
	if !intent.AllowSuggestions {
		suggestions = nil
	}
	if suggestions == nil {
		suggestions = []ai.SuggestedAction{}
	} else if validateSuggestions != nil {
		suggestions = validateSuggestions(suggestions)
	}

	c.JSON(http.StatusOK, gin.H{
		"analysis":          analysis,
		"suggested_actions": suggestions,
		"intent":            intent.Key,
		"model":             resp.Model,
		"redactions":        redactions,
	})
}

func (s *Server) aiAssistDeployment(c *gin.Context) {
	if s.aiProvider == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI assistant is not enabled", "code": "ai_disabled"})
		return
	}
	name := c.Param("name")

	var req assistRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Intent == "" {
		req.Intent = "diagnose"
	}
	if len(req.Sources) == 0 {
		req.Sources = []assistSource{{Type: "logs"}, {Type: "compose"}}
	}

	if _, err := s.manager.GetDeployment(name); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}

	sections := make([]ai.Section, 0, len(req.Sources)+1)
	sections = append(sections, s.platformSection(name))
	for _, src := range req.Sources {
		section, aerr := s.collectDeploymentSource(name, src)
		if aerr != nil {
			respondAPIError(c, aerr)
			return
		}
		sections = append(sections, section)
	}

	s.runAssist(c, "deployment "+name, sections, req, s.deploymentSecretValues(name),
		func(suggestions []ai.SuggestedAction) []ai.SuggestedAction {
			return s.filterSuggestionsForDeployment(name, suggestions)
		})
}

// aiAssistSystem analyzes caller-provided output at host or agent
// level. The agent adds no context of its own, so any actor who could
// see the output may request the analysis. Suggestions are dropped
// because there is no deployment to validate them against.
func (s *Server) aiAssistSystem(c *gin.Context) {
	if s.aiProvider == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI assistant is not enabled", "code": "ai_disabled"})
		return
	}

	var req assistRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Intent == "" {
		req.Intent = "diagnose"
	}

	sections := make([]ai.Section, 0, len(req.Sources)+1)
	sections = append(sections, s.platformSection(""))
	for _, src := range req.Sources {
		if src.Type != "provided" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "system scope only accepts provided sources"})
			return
		}
		if strings.TrimSpace(src.Content) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "provided source requires content"})
			return
		}
		label := src.Label
		if label == "" {
			label = "Provided output"
		}
		sections = append(sections, ai.Section{Label: label, Content: src.Content})
	}
	if len(sections) == 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "at least one provided source is required"})
		return
	}

	s.runAssist(c, "the FlatRun host", sections, req, s.systemSecretValues(),
		func([]ai.SuggestedAction) []ai.SuggestedAction { return []ai.SuggestedAction{} })
}

func (s *Server) systemSecretValues() []string {
	return []string{
		s.config.AI.APIKey,
		s.config.Auth.JWTSecret,
		s.config.Infrastructure.Database.RootPassword,
		s.config.Infrastructure.Redis.Password,
		s.config.Infrastructure.PowerDNS.APIKey,
	}
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
	return append(secrets, s.systemSecretValues()...)
}

// filterSuggestionsForDeployment drops suggestions naming services
// that do not exist in the deployment's compose file, so a
// hallucinated service name can never be acted on.
func (s *Server) filterSuggestionsForDeployment(name string, suggestions []ai.SuggestedAction) []ai.SuggestedAction {
	if len(suggestions) == 0 {
		return []ai.SuggestedAction{}
	}
	serviceNames, err := s.manager.GetComposeServiceNames(name)
	if err != nil {
		return []ai.SuggestedAction{}
	}
	known := make(map[string]bool, len(serviceNames))
	for _, sn := range serviceNames {
		known[sn] = true
	}
	valid := make([]ai.SuggestedAction, 0, len(suggestions))
	for _, sg := range suggestions {
		if known[sg.Service] {
			valid = append(valid, sg)
		}
	}
	return valid
}
