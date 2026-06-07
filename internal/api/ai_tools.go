package api

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/flatrun/agent/internal/ai"
	"github.com/flatrun/agent/internal/auth"
	"github.com/gin-gonic/gin"
)

const maxToolOutputChars = 8000

// aiTool is one read-only investigation capability the model can call
// to discover facts about this installation instead of guessing.
type aiTool struct {
	Spec ai.Tool
	Run  func(s *Server, c *gin.Context, boundDeployment string, args map[string]interface{}) (string, error)
}

// destructiveCommand matches obviously state-changing shell tokens, so
// an auto-run exec can never mutate the system even if the model asks.
var destructiveCommand = regexp.MustCompile(`(?i)\b(rm|rmdir|mv|dd|mkfs|truncate|tee|chmod|chown|kill|killall|pkill|shutdown|reboot|halt|apt|apt-get|yum|apk|dnf|systemctl|service|drop|delete|update|insert|alter)\b|>>?|\bmkdir\b`)

func truncateToolOutput(s string) string {
	if len(s) <= maxToolOutputChars {
		return s
	}
	return s[:maxToolOutputChars] + "\n[... output truncated ...]"
}

func toolDeployment(boundDeployment string, args map[string]interface{}) string {
	if name, ok := args["deployment"].(string); ok && name != "" {
		return name
	}
	return boundDeployment
}

// toolAllowedDeployment resolves the target deployment and verifies the
// current actor may read it. Returns "" plus an error message string
// the model sees when access is denied.
func (s *Server) toolAllowedDeployment(c *gin.Context, boundDeployment string, args map[string]interface{}) (string, error) {
	name := toolDeployment(boundDeployment, args)
	if name == "" {
		return "", fmt.Errorf("no deployment specified")
	}
	actor := auth.GetActorFromContext(c)
	if actor != nil && actor.Role != auth.RoleAdmin && !actor.CanAccessDeployment(name, auth.AccessLevelRead) {
		return "", fmt.Errorf("you do not have access to deployment %q", name)
	}
	return name, nil
}

func argString(args map[string]interface{}, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func (s *Server) aiToolRegistry() map[string]aiTool {
	objSchema := func(props map[string]interface{}, required ...string) map[string]interface{} {
		schema := map[string]interface{}{"type": "object", "properties": props}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	strProp := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "string", "description": desc}
	}

	return map[string]aiTool{
		"list_networks": {
			Spec: ai.Tool{
				Name:        "list_networks",
				Description: "List the Docker networks that currently exist on this host, with their drivers.",
				Parameters:  objSchema(map[string]interface{}{}),
			},
			Run: func(s *Server, _ *gin.Context, _ string, _ map[string]interface{}) (string, error) {
				networks, err := s.networksManager.ListNetworks()
				if err != nil {
					return "", err
				}
				var b strings.Builder
				for _, n := range networks {
					fmt.Fprintf(&b, "- %s (driver: %s)\n", n.Name, n.Driver)
				}
				if b.Len() == 0 {
					return "No networks found.", nil
				}
				return b.String(), nil
			},
		},
		"list_deployments": {
			Spec: ai.Tool{
				Name:        "list_deployments",
				Description: "List all deployments managed by this FlatRun instance with their current status.",
				Parameters:  objSchema(map[string]interface{}{}),
			},
			Run: func(s *Server, _ *gin.Context, _ string, _ map[string]interface{}) (string, error) {
				deployments, err := s.manager.ListDeployments()
				if err != nil {
					return "", err
				}
				var b strings.Builder
				for _, d := range deployments {
					fmt.Fprintf(&b, "- %s (status: %s)\n", d.Name, d.Status)
				}
				if b.Len() == 0 {
					return "No deployments found.", nil
				}
				return b.String(), nil
			},
		},
		"get_platform_config": {
			Spec: ai.Tool{
				Name:        "get_platform_config",
				Description: "Get this installation's FlatRun configuration: the proxy and database network names, whether the managed nginx reverse proxy and shared database are enabled, and the database host and type.",
				Parameters:  objSchema(map[string]interface{}{}),
			},
			Run: func(s *Server, _ *gin.Context, _ string, _ map[string]interface{}) (string, error) {
				return s.platformSection("").Content, nil
			},
		},
		"get_deployment_metadata": {
			Spec: ai.Tool{
				Name:        "get_deployment_metadata",
				Description: "Get a deployment's FlatRun metadata: its reverse-proxy routing (which service and container port each domain forwards to), exposed domains, health check path and databases. This is the source of truth for routing, not the compose expose field.",
				Parameters: objSchema(map[string]interface{}{
					"deployment": strProp("Deployment name. Omit to use the session's deployment."),
				}),
			},
			Run: func(s *Server, c *gin.Context, bound string, args map[string]interface{}) (string, error) {
				name, err := s.toolAllowedDeployment(c, bound, args)
				if err != nil {
					return "", err
				}
				return s.platformSection(name).Content, nil
			},
		},
		"get_deployment_logs": {
			Spec: ai.Tool{
				Name:        "get_deployment_logs",
				Description: "Get a deployment's recent container logs. In FlatRun, application logs are the containers' stdout/stderr captured by Docker, not files on disk, so use this tool to read logs rather than searching the filesystem.",
				Parameters: objSchema(map[string]interface{}{
					"deployment": strProp("Deployment name. Omit to use the session's deployment."),
					"tail":       map[string]interface{}{"type": "integer", "description": "How many recent lines to fetch (default 300, max 1000)."},
				}),
			},
			Run: func(s *Server, c *gin.Context, bound string, args map[string]interface{}) (string, error) {
				name, err := s.toolAllowedDeployment(c, bound, args)
				if err != nil {
					return "", err
				}
				tail := 300
				if v, ok := args["tail"].(float64); ok && int(v) > 0 {
					tail = int(v)
				}
				if tail > 1000 {
					tail = 1000
				}
				logs, err := s.manager.GetDeploymentLogs(name, tail)
				if err != nil {
					return "", err
				}
				if strings.TrimSpace(logs) == "" {
					return "The deployment has produced no logs.", nil
				}
				redactor := ai.NewRedactor(s.deploymentSecretValues(name))
				redacted, _ := redactor.Redact(logs)
				return truncateToolOutput(redacted), nil
			},
		},
		"list_deployment_files": {
			Spec: ai.Tool{
				Name:        "list_deployment_files",
				Description: "List files and directories inside a deployment's directory at the given path. Use this to find application-generated logs and data files mounted in the deployment.",
				Parameters: objSchema(map[string]interface{}{
					"deployment": strProp("Deployment name. Omit to use the session's deployment."),
					"path":       strProp("Path relative to the deployment directory. Defaults to the root."),
				}),
			},
			Run: func(s *Server, c *gin.Context, bound string, args map[string]interface{}) (string, error) {
				name, err := s.toolAllowedDeployment(c, bound, args)
				if err != nil {
					return "", err
				}
				path := argString(args, "path")
				if path == "" {
					path = "/"
				}
				files, err := s.filesManager.ListFiles(name, path)
				if err != nil {
					return "", err
				}
				var b strings.Builder
				for _, f := range files {
					kind := "file"
					if f.IsDir {
						kind = "dir"
					}
					fmt.Fprintf(&b, "- %s (%s, %d bytes)\n", f.Path, kind, f.Size)
				}
				if b.Len() == 0 {
					return "Directory is empty.", nil
				}
				return b.String(), nil
			},
		},
		"read_deployment_file": {
			Spec: ai.Tool{
				Name:        "read_deployment_file",
				Description: "Read a text file inside a deployment's directory, for example an application log file or config the app generated. Secret values are redacted.",
				Parameters: objSchema(map[string]interface{}{
					"deployment": strProp("Deployment name. Omit to use the session's deployment."),
					"path":       strProp("Path to the file relative to the deployment directory."),
				}, "path"),
			},
			Run: func(s *Server, c *gin.Context, bound string, args map[string]interface{}) (string, error) {
				name, err := s.toolAllowedDeployment(c, bound, args)
				if err != nil {
					return "", err
				}
				path := argString(args, "path")
				if path == "" {
					return "", fmt.Errorf("path is required")
				}
				reader, info, err := s.filesManager.ReadFile(name, path)
				if err != nil {
					return "", err
				}
				defer reader.Close()
				if info.IsDir {
					return "", fmt.Errorf("%s is a directory; use list_deployment_files", path)
				}
				buf := make([]byte, maxToolOutputChars)
				n, _ := reader.Read(buf)
				redactor := ai.NewRedactor(s.deploymentSecretValues(name))
				content, _ := redactor.Redact(string(buf[:n]))
				return truncateToolOutput(content), nil
			},
		},
		"exec_in_service": {
			Spec: ai.Tool{
				Name:        "exec_in_service",
				Description: "Run a READ-ONLY shell command inside a deployment's service container to inspect its state (for example: ls, cat, env, ps, netstat, curl localhost). Commands that change state are refused. Secret values in the output are redacted.",
				Parameters: objSchema(map[string]interface{}{
					"deployment": strProp("Deployment name. Omit to use the session's deployment."),
					"service":    strProp("Compose service name to run the command in."),
					"command":    strProp("The read-only shell command to run."),
				}, "service", "command"),
			},
			Run: func(s *Server, c *gin.Context, bound string, args map[string]interface{}) (string, error) {
				name, err := s.toolAllowedDeployment(c, bound, args)
				if err != nil {
					return "", err
				}
				service := argString(args, "service")
				command := argString(args, "command")
				if service == "" || command == "" {
					return "", fmt.Errorf("service and command are required")
				}
				if destructiveCommand.MatchString(command) {
					return "", fmt.Errorf("refused: %q looks like it changes state; only read-only commands are allowed", command)
				}
				resolved, err := s.manager.ResolveService(name, service)
				if err != nil {
					return "", err
				}
				if blocked, reason, perr := s.protectedDeploymentActionBlocked(name, protectedActionExec); perr == nil && blocked {
					return "", fmt.Errorf("%s", reason)
				}
				ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
				defer cancel()
				output, err := s.manager.ComposeExec(ctx, name, resolved, command)
				if err != nil {
					return "", err
				}
				redactor := ai.NewRedactor(s.deploymentSecretValues(name))
				redacted, _ := redactor.Redact(output)
				return truncateToolOutput(redacted), nil
			},
		},
	}
}

// aiToolSpecs returns the tool schemas to advertise to the model, in a
// stable order.
func (s *Server) aiToolSpecs() []ai.Tool {
	registry := s.aiToolRegistry()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	specs := make([]ai.Tool, 0, len(names))
	for _, name := range names {
		specs = append(specs, registry[name].Spec)
	}
	return specs
}

// runAITool executes one tool call, returning the textual result the
// model reads. Errors are returned as content (prefixed) so the model
// can recover rather than the loop aborting.
func (s *Server) runAITool(c *gin.Context, boundDeployment string, call ai.ToolCall) string {
	tool, ok := s.aiToolRegistry()[call.Name]
	if !ok {
		return "Error: unknown tool " + call.Name
	}
	var args map[string]interface{}
	if strings.TrimSpace(call.Arguments) != "" {
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			return "Error: could not parse tool arguments: " + err.Error()
		}
	}
	result, err := tool.Run(s, c, boundDeployment, args)
	if err != nil {
		return "Error: " + err.Error()
	}
	return result
}
