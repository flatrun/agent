package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/flatrun/agent/internal/ai"
	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/setup"
	"github.com/flatrun/agent/pkg/pluginapi"
	"github.com/gin-gonic/gin"
)

const maxToolOutputChars = 8000

// aiTool is one capability the model can call: read-only investigation
// by default, or a state change when Mutates is set.
type aiTool struct {
	Spec ai.Tool
	// Mutates marks a tool that changes state. A batch containing one always
	// pauses for operator approval, even in an auto-run session.
	Mutates bool
	Run     func(s *Server, c *gin.Context, boundDeployment string, args map[string]interface{}) (string, error)
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
		"get_instance_info": {
			Spec: ai.Tool{
				Name:        "get_instance_info",
				Description: "Get information about the FlatRun host itself: its hostname and public IP address.",
				Parameters:  objSchema(map[string]interface{}{}),
			},
			Run: func(s *Server, _ *gin.Context, _ string, _ map[string]interface{}) (string, error) {
				hostname, _ := os.Hostname()
				var b strings.Builder
				fmt.Fprintf(&b, "Hostname: %s\n", hostname)
				fmt.Fprintf(&b, "Public IP: %s\n", setup.ResolvePublicIP())
				return b.String(), nil
			},
		},
		"run_host_command": {
			Spec: ai.Tool{
				Name:        "run_host_command",
				Description: "Run a READ-ONLY shell command on the FlatRun host (not inside a container) to inspect the machine, for example: ip addr, hostname -I, df -h, docker ps, free -m. Commands that change state are refused. Requires system access.",
				Parameters: objSchema(map[string]interface{}{
					"command": strProp("The read-only shell command to run on the host."),
				}, "command"),
			},
			Run: func(s *Server, c *gin.Context, _ string, args map[string]interface{}) (string, error) {
				command := argString(args, "command")
				if command == "" {
					return "", fmt.Errorf("command is required")
				}
				actor := auth.GetActorFromContext(c)
				if actor != nil && actor.Role != auth.RoleAdmin && !actor.HasPermission(auth.PermSystemRead) {
					return "", fmt.Errorf("running host commands requires system access, which you do not have")
				}
				if s.config.SystemTerminal.ProtectedMode.Enabled && s.config.SystemTerminal.ProtectedMode.DisableTerminal {
					return "", fmt.Errorf("the system terminal is disabled by global protected mode")
				}
				if destructiveCommand.MatchString(command) {
					return "", fmt.Errorf("refused: %q looks like it changes state; only read-only commands are allowed", command)
				}
				if blocked, rule, _ := protectedCommandBlocked(&s.config.SystemTerminal.ProtectedMode, command); blocked {
					return "", fmt.Errorf("command blocked by global protected mode: %s", protectedCommandBlockMessage(command, rule))
				}
				ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "sh", "-c", command)
				cmd.Dir = s.config.DeploymentsPath
				out, err := cmd.CombinedOutput()
				redactor := ai.NewRedactor(s.systemSecretValues())
				redacted, _ := redactor.Redact(string(out))
				if err != nil {
					return truncateToolOutput(redacted) + "\n[command exited with error: " + err.Error() + "]", nil
				}
				return truncateToolOutput(redacted), nil
			},
		},
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
		"write_deployment_file": {
			Mutates: true,
			Spec: ai.Tool{
				Name:        "write_deployment_file",
				Description: "Create or overwrite a text file inside a deployment's directory, for example a config or compose file. Requires write access to the deployment; the path cannot escape the deployment directory.",
				Parameters: objSchema(map[string]interface{}{
					"deployment": strProp("Deployment name. Omit to use the session's deployment."),
					"path":       strProp("Path to the file relative to the deployment directory."),
					"content":    strProp("The full new contents of the file."),
				}, "path", "content"),
			},
			Run: func(s *Server, c *gin.Context, bound string, args map[string]interface{}) (string, error) {
				name, err := s.toolAllowedDeploymentWrite(c, bound, args)
				if err != nil {
					return "", err
				}
				path := argString(args, "path")
				if path == "" {
					return "", fmt.Errorf("path is required")
				}
				content, ok := args["content"].(string)
				if !ok {
					return "", fmt.Errorf("content is required")
				}
				if blocked, reason, perr := s.protectedDeploymentActionBlocked(name, protectedActionUploadFile); perr != nil {
					return "", fmt.Errorf("could not check protected mode: %w", perr)
				} else if blocked {
					return "", fmt.Errorf("%s", reason)
				}
				if err := s.filesManager.WriteFile(name, path, strings.NewReader(content)); err != nil {
					return "", err
				}
				return fmt.Sprintf("Wrote %d bytes to %s in %s.", len(content), path, name), nil
			},
		},
		"run_quick_action": {
			Mutates: true,
			Spec: ai.Tool{
				Name:        "run_quick_action",
				Description: "Run one of a deployment's configured quick actions by its id. Requires write access to the deployment.",
				Parameters: objSchema(map[string]interface{}{
					"deployment": strProp("Deployment name. Omit to use the session's deployment."),
					"action_id":  strProp("The id of the quick action to run."),
				}, "action_id"),
			},
			Run: func(s *Server, c *gin.Context, bound string, args map[string]interface{}) (string, error) {
				name, err := s.toolAllowedDeploymentWrite(c, bound, args)
				if err != nil {
					return "", err
				}
				actionID := argString(args, "action_id")
				if actionID == "" {
					return "", fmt.Errorf("action_id is required")
				}
				if blocked, reason, perr := s.protectedDeploymentActionBlocked(name, protectedActionQuickAction); perr != nil {
					return "", fmt.Errorf("could not check protected mode: %w", perr)
				} else if blocked {
					return "", fmt.Errorf("%s", reason)
				}
				output, err := s.manager.ExecuteQuickAction(name, actionID)
				if err != nil {
					return "", err
				}
				redactor := ai.NewRedactor(s.deploymentSecretValues(name))
				redacted, _ := redactor.Redact(output)
				return truncateToolOutput(redacted), nil
			},
		},
		"control_deployment": {
			Mutates: true,
			Spec: ai.Tool{
				Name:        "control_deployment",
				Description: "Start, stop, or restart a whole deployment. Requires write access to the deployment.",
				Parameters: objSchema(map[string]interface{}{
					"deployment": strProp("Deployment name. Omit to use the session's deployment."),
					"action": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"start", "stop", "restart"},
						"description": "What to do: start, stop, or restart.",
					},
				}, "action"),
			},
			Run: func(s *Server, c *gin.Context, bound string, args map[string]interface{}) (string, error) {
				name, err := s.toolAllowedDeploymentWrite(c, bound, args)
				if err != nil {
					return "", err
				}
				action := argString(args, "action")
				var output string
				switch action {
				case "start":
					output, err = s.manager.StartDeployment(name)
				case "stop":
					output, err = s.manager.StopDeployment(name)
				case "restart":
					output, err = s.manager.RestartDeployment(name)
				default:
					return "", fmt.Errorf("action must be start, stop, or restart")
				}
				if err != nil {
					return "", err
				}
				return truncateToolOutput(fmt.Sprintf("Ran %s on deployment %s.\n%s", action, name, output)), nil
			},
		},
		"get_security_events": {
			Spec: ai.Tool{
				Name:        "get_security_events",
				Description: "List recent security events for a deployment (blocked requests, scanners, auth failures) so they can be summarized. Read-only.",
				Parameters: objSchema(map[string]interface{}{
					"deployment": strProp("Deployment name. Omit to use the session's deployment."),
					"limit":      map[string]interface{}{"type": "integer", "description": "Maximum events to return (default 50)."},
				}),
			},
			Run: func(s *Server, c *gin.Context, bound string, args map[string]interface{}) (string, error) {
				name, err := s.toolAllowedDeployment(c, bound, args)
				if err != nil {
					return "", err
				}
				if s.securityManager == nil {
					return "The security module is not enabled.", nil
				}
				limit := 50
				if v, ok := args["limit"].(float64); ok && v > 0 {
					limit = int(v)
				}
				events, _, err := s.securityManager.GetEventsByDeployment(name, limit)
				if err != nil {
					return "", err
				}
				if len(events) == 0 {
					return fmt.Sprintf("No security events for %s.", name), nil
				}
				var b strings.Builder
				fmt.Fprintf(&b, "%d recent security events for %s:\n", len(events), name)
				for _, e := range events {
					fmt.Fprintf(&b, "- %s [%s] %s %s %d from %s: %s\n",
						e.CreatedAt.Format(time.RFC3339), e.Severity, e.RequestMethod, e.RequestPath, e.StatusCode, e.SourceIP, e.Message)
				}
				return truncateToolOutput(b.String()), nil
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
				if blocked, reason, perr := s.protectedDeploymentActionBlocked(name, protectedActionExec); perr != nil {
					return "", fmt.Errorf("could not check protected mode: %w", perr)
				} else if blocked {
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
// pluginToolPrefix namespaces plugin-provided tools so they never collide with built-ins and
// can be routed back to the owning plugin on dispatch: plugin__<plugin>__<tool>.
const pluginToolPrefix = "plugin__"

func pluginToolName(plugin, tool string) string {
	return pluginToolPrefix + plugin + "__" + tool
}

// parsePluginToolName splits a namespaced tool name back into plugin and tool.
func parsePluginToolName(name string) (plugin, tool string, ok bool) {
	rest, found := strings.CutPrefix(name, pluginToolPrefix)
	if !found {
		return "", "", false
	}
	plugin, tool, found = strings.Cut(rest, "__")
	return plugin, tool, found
}

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

	// Append tools contributed by running plugins.
	for _, info := range s.pluginInfos() {
		for _, t := range info.Tools {
			specs = append(specs, ai.Tool{
				Name:        pluginToolName(info.Name, t.Name),
				Description: t.Description,
				Parameters:  t.Parameters,
			})
		}
	}
	return specs
}

// pluginInfos returns running plugin infos, tolerating a nil host (as in unit tests).
func (s *Server) pluginInfos() []pluginapi.Info {
	if s.pluginHost == nil {
		return nil
	}
	return s.pluginHost.Infos()
}

// pluginToolSpec returns the advertised spec for a namespaced plugin tool.
func (s *Server) pluginToolSpec(plugin, tool string) (pluginapi.ToolSpec, bool) {
	for _, info := range s.pluginInfos() {
		if info.Name != plugin {
			continue
		}
		for _, t := range info.Tools {
			if t.Name == tool {
				return t, true
			}
		}
	}
	return pluginapi.ToolSpec{}, false
}

// runAITool executes one tool call, returning the textual result the
// model reads. Errors are returned as content (prefixed) so the model
// can recover rather than the loop aborting.
func (s *Server) runAITool(c *gin.Context, boundDeployment string, call ai.ToolCall) string {
	var args map[string]interface{}
	if strings.TrimSpace(call.Arguments) != "" {
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			return "Error: could not parse tool arguments: " + err.Error()
		}
	}

	// Plugin-provided tools are dispatched to the owning plugin over its socket.
	if plugin, tool, ok := parsePluginToolName(call.Name); ok {
		if spec, found := s.pluginToolSpec(plugin, tool); found && spec.Mutates {
			if spec.Global {
				// A host-wide setting has no deployment scope; gate it on settings-write so
				// it works in an unscoped assistant session instead of demanding a deployment.
				if err := s.toolAllowedGlobalWrite(c); err != nil {
					return "Error: " + err.Error()
				}
			} else {
				// A state-changing tool requires write access to a deployment, and the plugin is
				// told which one so it can scope the mutation to that deployment rather than act
				// on an arbitrary resource the caller named.
				dep, err := s.toolAllowedDeploymentWrite(c, boundDeployment, args)
				if err != nil {
					return "Error: " + err.Error()
				}
				if args == nil {
					args = map[string]interface{}{}
				}
				args["_deployment"] = dep
			}
		}
		result, err := s.pluginHost.ExecTool(plugin, tool, args)
		if err != nil {
			return "Error: " + err.Error()
		}
		return truncateToolOutput(result)
	}

	tool, ok := s.aiToolRegistry()[call.Name]
	if !ok {
		return "Error: unknown tool " + call.Name
	}
	result, err := tool.Run(s, c, boundDeployment, args)
	if err != nil {
		return "Error: " + err.Error()
	}
	return result
}

// toolMutates reports whether a named tool changes state, covering both
// built-in tools and plugin-contributed ones.
func (s *Server) toolMutates(name string) bool {
	if plugin, tool, ok := parsePluginToolName(name); ok {
		spec, found := s.pluginToolSpec(plugin, tool)
		return found && spec.Mutates
	}
	t, ok := s.aiToolRegistry()[name]
	return ok && t.Mutates
}

// toolAllowedDeploymentWrite resolves the target deployment and verifies the actor may write
// to it, gating a mutating plugin tool.
func (s *Server) toolAllowedDeploymentWrite(c *gin.Context, boundDeployment string, args map[string]interface{}) (string, error) {
	name := toolDeployment(boundDeployment, args)
	if name == "" {
		return "", fmt.Errorf("no deployment specified")
	}
	actor := auth.GetActorFromContext(c)
	if actor != nil && actor.Role != auth.RoleAdmin && !actor.CanAccessDeployment(name, auth.AccessLevelWrite) {
		return "", fmt.Errorf("you do not have write access to deployment %q", name)
	}
	return name, nil
}

// toolAllowedGlobalWrite gates a mutating tool that changes a host-wide setting rather than a
// deployment. It needs settings-write; a nil actor means auth is disabled, which is allowed as
// elsewhere.
func (s *Server) toolAllowedGlobalWrite(c *gin.Context) error {
	actor := auth.GetActorFromContext(c)
	if actor != nil && !actor.HasPermission(auth.PermSettingsWrite) {
		return fmt.Errorf("you do not have permission to change this setting")
	}
	return nil
}
