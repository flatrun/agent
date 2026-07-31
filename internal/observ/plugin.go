package observ

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
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
	isManaged := func(deployment string) bool {
		if deployment == "" || dataDir == "" {
			return false
		}
		info, err := os.Stat(filepath.Join(dataDir, deployment))
		return err == nil && info.IsDir()
	}
	watcher.SetManaged(isManaged)

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

	// History outlives the in-memory window and the process. If it cannot be opened the
	// engine still runs on the live window alone, since losing history is better than
	// losing the metrics and the self-healing with it.
	var history *MetricsDB
	if db, err := OpenMetricsDB(dataDir); err != nil {
		log.Printf("observability: metrics history unavailable, keeping the live window only: %v", err)
	} else {
		defer db.Close()
		history = db
		store.OnRecord(func(points []LatestPoint) {
			if err := db.WriteBatch(points); err != nil {
				log.Printf("observability: failed to store metrics: %v", err)
			}
		})
		stop := make(chan struct{})
		defer close(stop)
		go db.Maintain(stop, cfg.retention())
	}

	// Push to an OTLP backend when one is configured. A failure here leaves the metrics
	// collected, stored and scrapeable, so a backend being unreachable never costs FlatRun
	// its own observability.
	if endpoint := cfg.OTLPEndpoint; endpoint != "" || os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		if shutdown, err := StartOTLPExport(ctx, store, endpoint); err != nil {
			log.Printf("observability: OTLP export unavailable, metrics remain scrapeable: %v", err)
		} else {
			defer func() {
				flush, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = shutdown(flush)
			}()
		}
	}

	// Threshold rules over the same metrics the views draw. They are what turns collected
	// numbers into something that reaches an operator who is not looking at the screen.
	alertStore := NewAlertStore(dataDir)
	engine := NewAlertEngine(store)
	engine.SetRules(alertStore.Load())
	engine.OnAlert(func(ev AlertEvent) {
		emitNotificationTo(ev.RuleName, ev.Message(), ev.Targets)
	})
	// An opt-in rule action restarts the offending deployment when it fires,
	// scoped to FlatRun-managed deployments and rate-limited so it cannot flap.
	actioner := NewActionRunner(DockerComposeRestart, isManaged, cfg.restartCooldown(), dataDir)
	engine.OnAction(func(ev AlertEvent) {
		if msg := actioner.Run(ev); msg != "" {
			emitNotificationTo(ev.RuleName, msg, ev.Targets)
		}
	})
	alertStop := make(chan struct{})
	defer close(alertStop)
	go engine.Run(alertStop, cfg.sampleInterval()*3)

	go collector.Run(ctx)
	go NewHostCollector(store, SystemHostSource, cfg.sampleInterval()).Run(ctx)
	go watcher.Run(ctx)

	applyConfig := func(c Config) {
		watcher.SetEnabled(c.AutoRestart)
	}

	handler := HandlerWithAlerts(store, history, watcher, cfgStore, applyConfig, alerts{engine: engine, store: alertStore})
	return pluginsdk.Serve(PluginInfo, handler, buildTools(store, watcher, cfgStore)...)
}

// emitNotification asks the core to deliver a notification to the operator's configured
// targets. Delivery config and routing live in the agent, not the plugin.
func emitNotification(title, message string) {
	emitNotificationTo(title, message, nil)
}

// emitNotificationTo delivers to a chosen subset of targets by id; nil means all.
func emitNotificationTo(title, message string, targets []string) {
	base, token := pluginsdk.AgentCallback()
	if base == "" || token == "" {
		return
	}
	body, _ := json.Marshal(map[string]any{"title": title, "message": message, "targets": targets})
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
