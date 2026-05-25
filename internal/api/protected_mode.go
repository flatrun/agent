package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

const (
	protectedActionDeleteDeployment = "delete_deployment"
	protectedActionUpdateDeployment = "update_deployment"
	protectedActionUpdateMetadata   = "update_metadata"
	protectedActionUpdateEnv        = "update_env"
	protectedActionDeleteFile       = "delete_file"
	protectedActionUploadFile       = "upload_file"
	protectedActionCreateDir        = "create_dir"
	protectedActionQuickAction      = "quick_action"
	protectedActionTerminal         = "terminal"
	protectedActionExec             = "exec"
	protectedActionRebuild          = "rebuild_deployment"
)

var defaultProtectedActions = map[string]struct{}{
	protectedActionDeleteDeployment: {},
	protectedActionUpdateDeployment: {},
	protectedActionUpdateEnv:        {},
	protectedActionDeleteFile:       {},
	protectedActionRebuild:          {},
}

var protectedCommandRuleMatches = map[string]struct{}{
	"contains": {},
	"equals":   {},
	"prefix":   {},
	"suffix":   {},
	"matches":  {},
}

func (s *Server) requireUnprotectedDeploymentAction(c *gin.Context, deploymentName, action string) bool {
	blocked, reason, err := s.protectedDeploymentActionBlocked(deploymentName, action)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return false
	}
	if blocked {
		c.JSON(http.StatusLocked, gin.H{
			"error":  reason,
			"action": action,
			"reason": reason,
		})
		return false
	}
	return true
}

func (s *Server) protectedDeploymentActionBlocked(deploymentName, action string) (bool, string, error) {
	deployment, err := s.manager.GetDeployment(deploymentName)
	if err != nil {
		return false, "", err
	}
	if !deploymentProtectedModeEnabled(deployment.Metadata) {
		return false, "", nil
	}
	if protectedActionBlocked(deployment.Metadata.ProtectedMode, action) {
		return true, protectedActionBlockedReason(deployment.Metadata.ProtectedMode, action), nil
	}
	return false, "", nil
}

func deploymentProtectedModeEnabled(metadata *models.ServiceMetadata) bool {
	return metadata != nil && metadata.ProtectedMode != nil && metadata.ProtectedMode.Enabled
}

func protectedActionBlocked(cfg *models.ProtectedModeConfig, action string) bool {
	if cfg == nil || !cfg.Enabled {
		return false
	}
	for _, blocked := range cfg.BlockedActions {
		if strings.EqualFold(strings.TrimSpace(blocked), action) {
			return true
		}
	}
	if action == protectedActionTerminal && cfg.DisableTerminal {
		return true
	}
	if len(cfg.BlockedActions) > 0 {
		return false
	}
	_, blocked := defaultProtectedActions[action]
	return blocked
}

func protectedActionBlockedReason(cfg *models.ProtectedModeConfig, action string) string {
	if action == protectedActionTerminal && cfg != nil && cfg.DisableTerminal {
		return "Terminal access is disabled for this deployment by protected mode settings"
	}
	return fmt.Sprintf("%s is blocked for this deployment", protectedActionLabel(action))
}

func protectedActionLabel(action string) string {
	switch action {
	case protectedActionDeleteDeployment:
		return "Deleting this deployment"
	case protectedActionUpdateDeployment:
		return "Editing the compose configuration"
	case protectedActionUpdateMetadata:
		return "Updating deployment metadata"
	case protectedActionUpdateEnv:
		return "Editing environment variables"
	case protectedActionDeleteFile:
		return "Deleting deployment files"
	case protectedActionUploadFile:
		return "Uploading deployment files"
	case protectedActionCreateDir:
		return "Creating deployment directories"
	case protectedActionQuickAction:
		return "Running quick actions"
	case protectedActionTerminal:
		return "Opening the terminal"
	case protectedActionExec:
		return "Executing container commands"
	case protectedActionRebuild:
		return "Rebuilding this deployment"
	default:
		return "This action"
	}
}

func validateProtectedModeConfig(cfg *models.ProtectedModeConfig) error {
	if cfg == nil {
		return nil
	}
	for i, rule := range cfg.BlockedCommandRules {
		match := strings.ToLower(strings.TrimSpace(rule.Match))
		if match == "" {
			return fmt.Errorf("blocked_command_rules[%d].match is required", i)
		}
		if _, ok := protectedCommandRuleMatches[match]; !ok {
			return fmt.Errorf("blocked_command_rules[%d].match must be one of: contains, equals, prefix, suffix, matches", i)
		}
		if strings.TrimSpace(rule.Pattern) == "" {
			return fmt.Errorf("blocked_command_rules[%d].pattern is required", i)
		}
		if match == "matches" {
			if _, err := regexp.Compile(rule.Pattern); err != nil {
				return fmt.Errorf("blocked_command_rules[%d].pattern is not a valid regex: %w", i, err)
			}
		}
	}
	return nil
}

func protectedCommandBlocked(cfg *models.ProtectedModeConfig, command string) (bool, *models.ProtectedCommandRule, error) {
	if cfg == nil || !cfg.Enabled {
		return false, nil, nil
	}
	normalizedCommand := strings.ToLower(strings.Join(strings.Fields(command), " "))
	for _, rule := range cfg.BlockedCommandRules {
		ruleCopy := rule
		matched, err := protectedCommandRuleMatchesCommand(ruleCopy, command, normalizedCommand)
		if err != nil {
			return false, nil, err
		}
		if matched {
			return true, &ruleCopy, nil
		}
	}
	return false, nil, nil
}

func protectedCommandBlockMessage(command string, rule *models.ProtectedCommandRule) string {
	if rule == nil {
		return fmt.Sprintf("Command blocked: %s", command)
	}
	ruleLabel := rule.Name
	if ruleLabel == "" {
		ruleLabel = rule.ID
	}
	if ruleLabel == "" {
		return fmt.Sprintf("Command blocked: %s", command)
	}
	return fmt.Sprintf("Command blocked: %s (rule: %s)", command, ruleLabel)
}

func protectedCommandRuleMatchesCommand(rule models.ProtectedCommandRule, command, normalizedCommand string) (bool, error) {
	match := strings.ToLower(strings.TrimSpace(rule.Match))
	pattern := strings.Join(strings.Fields(rule.Pattern), " ")
	if match == "" || pattern == "" {
		return false, nil
	}

	target := normalizedCommand
	needle := strings.ToLower(pattern)
	if rule.CaseSensitive {
		target = strings.Join(strings.Fields(command), " ")
		needle = pattern
	}

	switch match {
	case "contains":
		return strings.Contains(target, needle), nil
	case "equals":
		return target == needle, nil
	case "prefix":
		return strings.HasPrefix(target, needle), nil
	case "suffix":
		return strings.HasSuffix(target, needle), nil
	case "matches":
		expr := pattern
		if !rule.CaseSensitive {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return false, err
		}
		return re.MatchString(command), nil
	default:
		return false, fmt.Errorf("unsupported protected command rule match type: %s", rule.Match)
	}
}

func (s *Server) protectedContainerCommandBlocked(containerID, command string) (bool, *models.ProtectedCommandRule, error) {
	deploymentName, err := containerDeploymentName(containerID)
	if err != nil || deploymentName == "" {
		return false, nil, err
	}
	deployment, err := s.manager.GetDeployment(deploymentName)
	if err != nil {
		return false, nil, err
	}
	if deployment.Metadata == nil {
		return false, nil, nil
	}
	return protectedCommandBlocked(deployment.Metadata.ProtectedMode, command)
}
