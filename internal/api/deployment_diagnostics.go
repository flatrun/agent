package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/security"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

type DiagnosticStatus string

const (
	diagnosticPassed  DiagnosticStatus = "passed"
	diagnosticFailed  DiagnosticStatus = "failed"
	diagnosticWarning DiagnosticStatus = "warning"
	diagnosticSkipped DiagnosticStatus = "skipped"
)

type DeploymentDiagnosticStep struct {
	ID      string           `json:"id"`
	Label   string           `json:"label"`
	Status  DiagnosticStatus `json:"status"`
	Detail  string           `json:"detail"`
	Output  string           `json:"output,omitempty"`
	Action  string           `json:"action,omitempty"`
	Value   string           `json:"value,omitempty"`
	Checked time.Time        `json:"checked_at"`
}

type DeploymentDiagnostics struct {
	Deployment string                     `json:"deployment"`
	IncidentID string                     `json:"incident_id,omitempty"`
	Healthy    bool                       `json:"healthy"`
	Steps      []DeploymentDiagnosticStep `json:"steps"`
	CheckedAt  time.Time                  `json:"checked_at"`
}

func (s *Server) diagnoseDeployment(c *gin.Context) {
	deployment, err := s.manager.GetDeployment(c.Param("name"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}

	incidentID := strings.ToUpper(strings.TrimSpace(c.Query("incident_id")))
	if incidentID != "" && !validIncidentID(incidentID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid incident ID"})
		return
	}

	actor := auth.GetActorFromContext(c)
	canReadSecurity := actor != nil && actor.HasPermission(auth.PermSecurityRead)
	canWriteSecurity := actor != nil && actor.HasPermission(auth.PermSecurityWrite)
	c.JSON(http.StatusOK, s.runDeploymentDiagnostics(c.Request.Context(), deployment, incidentID, canReadSecurity, canWriteSecurity))
}

func (s *Server) runDeploymentDiagnostics(ctx context.Context, deployment *models.Deployment, incidentID string, canReadSecurity, canWriteSecurity bool) DeploymentDiagnostics {
	now := time.Now()
	result := DeploymentDiagnostics{Deployment: deployment.Name, IncidentID: incidentID, Healthy: true, CheckedAt: now}
	add := func(id, label string, status DiagnosticStatus, detail, action, value string) {
		result.Steps = append(result.Steps, DeploymentDiagnosticStep{
			ID: id, Label: label, Status: status, Detail: detail, Action: action, Value: value, Checked: time.Now(),
		})
		if status == diagnosticFailed {
			result.Healthy = false
		}
	}
	addWithOutput := func(id, label string, status DiagnosticStatus, detail, output, action, value string) {
		add(id, label, status, detail, action, value)
		result.Steps[len(result.Steps)-1].Output = diagnosticOutput(output)
	}

	running := false
	for _, service := range deployment.Services {
		if service.Status == "running" {
			running = true
			break
		}
	}
	if running {
		add("container", "Container", diagnosticPassed, "At least one deployment service is running.", "", "")
	} else {
		add("container", "Container", diagnosticFailed, "No deployment service is running.", "start_deployment", deployment.Name)
	}

	dockerStatus := diagnosticPassed
	dockerDetail := "All configured Docker health checks pass."
	hasDockerHealth := false
	for _, service := range deployment.Services {
		if service.Health == "" {
			continue
		}
		hasDockerHealth = true
		if service.Health != "healthy" {
			dockerStatus = diagnosticFailed
			dockerDetail = fmt.Sprintf("Service %s reports Docker health %s.", service.Name, service.Health)
			break
		}
	}
	if !hasDockerHealth {
		dockerStatus = diagnosticWarning
		dockerDetail = "No Docker health check is configured in Compose."
	}
	add("docker_health", "Docker health", dockerStatus, dockerDetail, "edit_compose", "")

	s.addApplicationHealthDiagnostic(ctx, deployment, add, addWithOutput)

	proxyStatus := s.proxyOrchestrator.GetDeploymentProxyStatus(deployment)
	if !proxyStatus.Exposed {
		add("proxy", "Public route", diagnosticSkipped, "This deployment is not exposed publicly.", "", "")
	} else if !proxyStatus.VirtualHostExists {
		add("proxy", "Public route", diagnosticFailed, "The nginx virtual host is missing.", "configure_domain", proxyStatus.Domain)
	} else {
		add("proxy", "Public route", diagnosticPassed, "The nginx virtual host exists.", "", proxyStatus.Domain)
	}

	if proxyStatus.SSLEnabled {
		if proxyStatus.CertificateExists {
			add("tls", "TLS certificate", diagnosticPassed, "A certificate is installed for the public route.", "", proxyStatus.Domain)
		} else {
			add("tls", "TLS certificate", diagnosticFailed, "The public route requires TLS but has no certificate.", "renew_certificate", proxyStatus.Domain)
		}
	} else {
		add("tls", "TLS certificate", diagnosticSkipped, "TLS is not enabled for this deployment.", "", "")
	}

	if proxyStatus.Domain != "" && proxyStatus.VirtualHostExists {
		scheme := "http"
		if proxyStatus.SSLEnabled {
			scheme = "https"
		}
		client := &http.Client{Timeout: 8 * time.Second}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+proxyStatus.Domain+"/", nil)
		if err != nil {
			add("public", "Public response", diagnosticFailed, err.Error(), "view_logs", proxyStatus.Domain)
		} else if response, err := client.Do(req); err != nil {
			add("public", "Public response", diagnosticFailed, err.Error(), "view_logs", proxyStatus.Domain)
		} else {
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 400 {
				add("public", "Public response", diagnosticPassed, fmt.Sprintf("The public root returned HTTP %d.", response.StatusCode), "", proxyStatus.Domain)
			} else {
				add("public", "Public response", diagnosticFailed, fmt.Sprintf("The public root returned HTTP %d.", response.StatusCode), "view_logs", proxyStatus.Domain)
			}
		}
	}

	s.addSecurityDiagnostic(&result, deployment, proxyStatus.Domains, incidentID, canReadSecurity, canWriteSecurity)
	return result
}

func parseHealthResponse(output string) (string, int, error) {
	trimmed := strings.TrimSpace(output)
	separator := strings.LastIndex(trimmed, "\n")
	if separator < 0 {
		return "", 0, fmt.Errorf("missing HTTP status")
	}
	status, err := strconv.Atoi(strings.TrimSpace(trimmed[separator+1:]))
	if err != nil {
		return "", 0, err
	}
	return trimmed[:separator], status, nil
}

func healthStatusAccepted(status int, accepted []int) bool {
	if len(accepted) == 0 {
		return status >= 200 && status < 400
	}
	for _, candidate := range accepted {
		if status == candidate {
			return true
		}
	}
	return false
}

func healthBodyAccepted(body, expected string) bool {
	return expected == "" || strings.Contains(body, expected)
}

func diagnosticOutput(output string) string {
	const limit = 4096
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return "The endpoint returned an empty response body."
	}
	if len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit] + "\nResponse truncated."
}

func (s *Server) addSecurityDiagnostic(result *DeploymentDiagnostics, deployment *models.Deployment, domains []string, incidentID string, canReadSecurity, canWriteSecurity bool) {
	now := time.Now()
	step := DeploymentDiagnosticStep{ID: "security", Label: "Security decision", Status: diagnosticSkipped, Detail: "Enter an incident ID to inspect a visitor's request.", Checked: now}
	if s.securityManager == nil {
		step.Status = diagnosticSkipped
		step.Detail = "Security monitoring is disabled."
		result.Steps = append(result.Steps, step)
		return
	}
	if incidentID == "" {
		result.Steps = append(result.Steps, step)
		return
	}
	if !canReadSecurity {
		step.Status = diagnosticWarning
		step.Detail = "An administrator with security access must inspect this incident ID."
		result.Steps = append(result.Steps, step)
		return
	}

	event, err := s.securityManager.GetEventByIncidentID(incidentID)
	if err != nil || !incidentMatchesDeployment(event, deployment.Name, domains) {
		step.Status = diagnosticWarning
		step.Detail = "No security event for this deployment matches that incident ID."
		result.Steps = append(result.Steps, step)
		return
	}

	active, err := s.securityManager.GetActiveBlockedIPs()
	if err != nil {
		step.Status = diagnosticWarning
		step.Detail = "Active IP blocks could not be checked."
		result.Steps = append(result.Steps, step)
		return
	}
	blocks := make(map[string]security.BlockedIP, len(active))
	for _, blocked := range active {
		blocks[blocked.IP] = blocked
	}

	blocked, isBlocked := blocks[event.SourceIP]
	step.Status = diagnosticPassed
	step.Detail = fmt.Sprintf("Incident %s returned HTTP %d for %s from %s.", incidentID, event.StatusCode, event.RequestPath, event.SourceIP)
	if isBlocked && event.StatusCode == http.StatusForbidden {
		step.Status = diagnosticFailed
		step.Detail = fmt.Sprintf("Incident %s was denied by an active IP block for %s: %s", incidentID, event.SourceIP, blocked.Reason)
		if canWriteSecurity {
			step.Action = "unblock_ip"
			step.Value = event.SourceIP
		}
		result.Healthy = false
	}
	result.Steps = append(result.Steps, step)
}

func (s *Server) addApplicationHealthDiagnostic(
	ctx context.Context,
	deployment *models.Deployment,
	add func(string, string, DiagnosticStatus, string, string, string),
	addWithOutput func(string, string, DiagnosticStatus, string, string, string, string),
) {
	metadata := deployment.Metadata
	if metadata == nil || len(metadata.EffectiveHealthChecks()) == 0 {
		add("application", "Application health", diagnosticSkipped, "No application health check is configured in service.yml.", "edit_healthcheck", "")
		return
	}
	for _, config := range metadata.EffectiveHealthChecks() {
		s.addServiceHealthDiagnostic(ctx, deployment, config, add, addWithOutput)
	}
}

func (s *Server) addServiceHealthDiagnostic(
	ctx context.Context,
	deployment *models.Deployment,
	config models.HealthCheckConfig,
	add func(string, string, DiagnosticStatus, string, string, string),
	addWithOutput func(string, string, DiagnosticStatus, string, string, string, string),
) {
	metadata := deployment.Metadata
	checkType := healthCheckType(config)
	service := config.Service
	if service == "" {
		service = metadata.EffectivePrimaryService()
	}
	port := config.Port
	if port == 0 {
		port = metadata.Networking.ContainerPort
	}
	if service == "" || checkType != "exec" && (port < 1 || port > 65535) || checkType == "http" && !validHealthPath(config.Path) {
		add("application", "Application health: "+service, diagnosticWarning, "The service health configuration is incomplete.", "edit_healthcheck", service)
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	switch checkType {
	case "tcp":
		ip, err := s.manager.ContainerServiceIP(deployment.Name, service, "")
		if err != nil {
			addWithOutput("application", "Application health: "+service, diagnosticFailed, "The service container address could not be resolved.", err.Error(), "edit_healthcheck", service)
			return
		}
		connection, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(probeCtx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
		if err != nil {
			addWithOutput("application", "Application health: "+service, diagnosticFailed, fmt.Sprintf("TCP port %d did not accept a connection.", port), err.Error(), "edit_healthcheck", service)
			return
		}
		_ = connection.Close()
		add("application", "Application health: "+service, diagnosticPassed, fmt.Sprintf("TCP port %d accepted a connection.", port), "", service)
	case "exec":
		output, err := s.manager.ComposeExec(probeCtx, deployment.Name, service, config.Command)
		if err != nil {
			addWithOutput("application", "Application health: "+service, diagnosticFailed, "The health command returned an error.", output, "edit_healthcheck", service)
			return
		}
		add("application", "Application health: "+service, diagnosticPassed, "The health command completed successfully.", "", service)
	default:
		title := "Application health: " + service
		command := fmt.Sprintf("curl -sS -w '\\n%%{http_code}' --max-time 5 %s", shellLiteral("http://127.0.0.1:"+strconv.Itoa(port)+config.Path))
		output, err := s.manager.ComposeExec(probeCtx, deployment.Name, service, command)
		body, statusCode, parseErr := parseHealthResponse(output)
		if err != nil || parseErr != nil {
			addWithOutput("application", title, diagnosticFailed, "The configured endpoint could not be reached from its service container.", output, "edit_healthcheck", service)
		} else if healthStatusAccepted(statusCode, config.SuccessStatuses) && healthBodyAccepted(body, config.ResponseContains) {
			detail := fmt.Sprintf("GET %s returned HTTP %d.", config.Path, statusCode)
			if config.ResponseContains != "" {
				detail = fmt.Sprintf("GET %s returned HTTP %d and matched the expected response.", config.Path, statusCode)
			}
			add("application", title, diagnosticPassed, detail, "", service)
		} else if healthStatusAccepted(statusCode, config.SuccessStatuses) {
			addWithOutput("application", title, diagnosticFailed, fmt.Sprintf("GET %s returned HTTP %d but did not match the expected response.", config.Path, statusCode), body, "edit_healthcheck", service)
		} else if statusCode == http.StatusNotFound {
			addWithOutput("application", title, diagnosticWarning, fmt.Sprintf("GET %s returned HTTP 404. Configure a health endpoint to enable this check.", config.Path), body, "edit_healthcheck", service)
		} else {
			addWithOutput("application", title, diagnosticFailed, fmt.Sprintf("GET %s returned HTTP %d.", config.Path, statusCode), body, "edit_healthcheck", service)
		}
	}
}

func healthCheckType(config models.HealthCheckConfig) string {
	checkType := strings.ToLower(strings.TrimSpace(config.Type))
	if checkType == "" {
		return "http"
	}
	return checkType
}

func healthCheckConfigured(config models.HealthCheckConfig) bool {
	return strings.TrimSpace(config.Type) != "" || config.Path != "" || strings.TrimSpace(config.Command) != ""
}

var incidentIDPattern = regexp.MustCompile(`^FR-[A-F0-9]{12}$`)

func validIncidentID(value string) bool {
	return incidentIDPattern.MatchString(value)
}

func incidentMatchesDeployment(event *security.SecurityEvent, deployment string, domains []string) bool {
	if event == nil {
		return false
	}
	if event.DeploymentName == deployment {
		return true
	}
	for _, domain := range domains {
		if event.DeploymentName == domain {
			return true
		}
	}
	return false
}

func validHealthPath(value string) bool {
	if !strings.HasPrefix(value, "/") {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	return err == nil && !parsed.IsAbs()
}

func shellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
