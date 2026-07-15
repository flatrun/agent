package observ

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/flatrun/agent/pkg/pluginapi"
	"github.com/flatrun/agent/pkg/pluginsdk"
)

// PluginInfo identifies the observability app to the host and declares the UI it contributes:
// a metrics + health panel inside each deployment, and a settings form for its config.
var PluginInfo = pluginapi.Info{
	Name:         "observability",
	Version:      "0.1.0",
	DisplayName:  "Observability",
	Description:  "Per-deployment metrics and health, OpenTelemetry-native.",
	Capabilities: []string{"metrics", "docker"},
	ConfigSchema: ConfigSchema,
	UIExtensions: []pluginapi.UIExtension{
		{Slot: "deployment.detail", Kind: "metrics-panel", Title: "Metrics & Health", Icon: "activity", Endpoint: "/metrics/deployment"},
		{Slot: "settings", Kind: "form", Title: "Observability", Icon: "activity", Endpoint: "/config"},
	},
}

// RunPlugin collects metrics, watches container health, restarts unhealthy containers, and
// serves it all until the host stops the process. It is the entry point for both the
// standalone plugin binary and the agent's self-exec subcommand.
func RunPlugin() error {
	ctx := context.Background()
	cfgStore := NewConfigStore(os.Getenv(pluginapi.EnvDataDir))
	cfg := cfgStore.Load()

	dataDir := os.Getenv(pluginapi.EnvDataDir)
	store := NewStore(720)
	collector := NewCollector(store, DockerStatsSource, cfg.sampleInterval())
	watcher := NewHealthWatcher(DockerHealthSource, DockerRestart, 15*time.Second, cfg.restartCooldown())
	watcher.SetEnabled(cfg.AutoRestart)
	// A deployment FlatRun manages has a directory under the deployments path; only those are
	// eligible for auto-restart, so stray host containers are never touched.
	watcher.SetManaged(func(deployment string) bool {
		if deployment == "" || dataDir == "" {
			return false
		}
		info, err := os.Stat(filepath.Join(dataDir, deployment))
		return err == nil && info.IsDir()
	})

	watcher.OnRecover(func(ev RecoveryEvent) {
		emitNotification(
			fmt.Sprintf("Auto-recovered %s", ev.Container),
			fmt.Sprintf("Container %s in deployment %s was unhealthy and has been restarted.", ev.Container, ev.Deployment),
		)
	})

	watcher.OnExhausted(func(ev ExhaustedEvent) {
		emitNotification(
			fmt.Sprintf("Still unhealthy: %s", ev.Container),
			fmt.Sprintf("Container %s in deployment %s is still unhealthy after %d restart attempts. "+
				"Nothing further will be tried automatically, so it needs attention.",
				ev.Container, ev.Deployment, ev.Attempts),
		)
	})

	go collector.Run(ctx)
	go watcher.Run(ctx)

	applyConfig := func(c Config) {
		watcher.SetEnabled(c.AutoRestart)
	}

	return pluginsdk.Serve(PluginInfo, Handler(store, watcher, cfgStore, applyConfig), buildTools(store, watcher, cfgStore)...)
}

// emitNotification asks the core to deliver a notification to the operator's configured
// targets. Delivery config and routing live in the agent, not the plugin.
func emitNotification(title, message string) {
	base, token := pluginsdk.AgentCallback()
	if base == "" || token == "" {
		return
	}
	body, _ := json.Marshal(map[string]string{"title": title, "message": message})
	req, err := http.NewRequest(http.MethodPost, base+"/internal/notify/emit", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Plugin-Token", token)
	client := &http.Client{Timeout: 5 * time.Second}
	if resp, err := client.Do(req); err == nil {
		_ = resp.Body.Close()
	}
}
