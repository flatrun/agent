package observ

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/flatrun/agent/pkg/pluginapi"
	"github.com/flatrun/agent/pkg/pluginsdk"
)

// containerProject resolves the compose project (deployment) a container belongs to. It is a
// package var so tests can stub it without Docker.
var containerProject = dockerContainerProject

func dockerContainerProject(container string) (string, error) {
	out, err := exec.Command("docker", "inspect", "-f",
		`{{ index .Config.Labels "com.docker.compose.project" }}`, container).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// buildTools exposes observability capabilities to the AI assistant: read tools for health
// and metrics, and mutating tools to manage recovery. Mutating tools set Mutates so the host
// gates them on write access.
func buildTools(store *Store, watcher *HealthWatcher, cfgStore *ConfigStore) []pluginsdk.Tool {
	strParam := func(name, desc string, required bool) map[string]any {
		schema := map[string]any{
			"type":       "object",
			"properties": map[string]any{name: map[string]any{"type": "string", "description": desc}},
		}
		if required {
			schema["required"] = []string{name}
		}
		return schema
	}

	return []pluginsdk.Tool{
		{
			Spec: pluginapi.ToolSpec{
				Name:        "get_deployment_health",
				Description: "Report the health (healthy/unhealthy/starting) of each container in a deployment.",
				Parameters:  strParam("deployment", "The deployment name.", true),
			},
			Run: func(args map[string]any) (string, error) {
				dep := argStr(args, "deployment")
				var b strings.Builder
				found := false
				for _, h := range watcher.Snapshot() {
					if dep != "" && h.Deployment != dep {
						continue
					}
					found = true
					fmt.Fprintf(&b, "%s: %s\n", h.Container, h.Status)
				}
				if !found {
					return fmt.Sprintf("No running containers found for deployment %q.", dep), nil
				}
				return b.String(), nil
			},
		},
		{
			Spec: pluginapi.ToolSpec{
				Name:        "get_deployment_metrics",
				Description: "Summarize the latest CPU, memory and network usage per container in a deployment.",
				Parameters:  strParam("deployment", "The deployment name.", true),
			},
			Run: func(args map[string]any) (string, error) {
				dep := argStr(args, "deployment")
				groups := filterByDeployment(groupLatest(store.Latest()), dep)
				if len(groups) == 0 {
					return fmt.Sprintf("No metrics for deployment %q yet.", dep), nil
				}
				var b strings.Builder
				for _, g := range groups {
					for _, c := range g.Containers {
						fmt.Fprintf(&b, "%s: cpu %.1f%%, mem %s, net rx %s / tx %s\n",
							c.Container,
							c.Metrics[MetricCPUUsage],
							humanBytes(c.Metrics[MetricMemoryUsage]),
							humanBytes(c.Metrics[MetricNetworkRx]),
							humanBytes(c.Metrics[MetricNetworkTx]),
						)
					}
				}
				return b.String(), nil
			},
		},
		{
			Spec: pluginapi.ToolSpec{
				Name:        "set_auto_restart",
				Description: "Enable or disable automatically restarting unhealthy containers.",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{"enabled": map[string]any{"type": "boolean", "description": "Whether auto-restart is on."}},
					"required":   []string{"enabled"},
				},
				Mutates: true,
				Global:  true,
			},
			Run: func(args map[string]any) (string, error) {
				enabled, _ := args["enabled"].(bool)
				cfg := cfgStore.Load()
				cfg.AutoRestart = enabled
				if err := cfgStore.Save(cfg); err != nil {
					return "", err
				}
				watcher.SetEnabled(enabled)
				return fmt.Sprintf("Auto-restart is now %s.", onOff(enabled)), nil
			},
		},
		{
			Spec: pluginapi.ToolSpec{
				Name:        "restart_container",
				Description: "Restart a container by name.",
				Parameters:  strParam("container", "The container name to restart.", true),
				Mutates:     true,
			},
			Run: func(args map[string]any) (string, error) {
				container := argStr(args, "container")
				if container == "" {
					return "", fmt.Errorf("no container specified")
				}
				// _deployment is set by the agent to the write-authorized deployment; the
				// container must belong to it, so a caller cannot restart another
				// deployment's container by naming it.
				scope := argStr(args, "_deployment")
				if scope == "" {
					return "", fmt.Errorf("restarting a container requires a deployment-scoped session")
				}
				project, err := containerProject(container)
				if err != nil {
					return "", fmt.Errorf("could not resolve container %q: %w", container, err)
				}
				if project != scope {
					return "", fmt.Errorf("container %q is not part of deployment %q", container, scope)
				}
				if err := DockerRestart(container); err != nil {
					return "", fmt.Errorf("restart failed: %w", err)
				}
				return fmt.Sprintf("Restarted %s.", container), nil
			},
		},
	}
}

func argStr(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func humanBytes(v float64) string {
	switch {
	case v >= 1<<30:
		return fmt.Sprintf("%.1fG", v/(1<<30))
	case v >= 1<<20:
		return fmt.Sprintf("%.0fM", v/(1<<20))
	case v >= 1<<10:
		return fmt.Sprintf("%.0fK", v/(1<<10))
	default:
		return fmt.Sprintf("%.0fB", v)
	}
}
