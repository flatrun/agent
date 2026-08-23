package observ

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/flatrun/agent/pkg/pluginapi"
)

// healthReporter is the slice of the health watcher the API needs; nil-safe so the handler
// can be used in tests without a watcher.
type healthReporter interface {
	Snapshot() []ContainerHealth
	Events() []RecoveryEvent
}

// configAccess is the slice of the config store the API needs.
type configAccess interface {
	Load() Config
	Save(Config) error
}

// Handler serves the store, health, and config over JSON for the native FlatRun UI. It is
// mounted by the plugin and reached through the agent's plugin proxy. health, cfg, and apply
// may be nil. apply is invoked with the saved config so a live watcher/collector picks up a
// change without a restart.
// alertAccess is the slice of the alert engine and its store the API needs; nil-safe so the
// handler can be built without alerting.
type alertAccess interface {
	Rules() []AlertRule
	SetRules([]AlertRule)
	Events() []AlertEvent
	Firing() []AlertEvent
}

// alertPersistence saves the rules across restarts.
type alertPersistence interface {
	Load() []AlertRule
	Save([]AlertRule) error
}

// logRuleAccess is the slice of the log engine the API needs; nil-safe.
type logRuleAccess interface {
	Rules() []LogRule
	SetRules([]LogRule)
	Incidents() []Incident
}

type logRulePersistence interface {
	Load() []LogRule
	Save([]LogRule) error
}

// alerts is optional wiring for the rule endpoints.
type alerts struct {
	engine    alertAccess
	store     alertPersistence
	logEngine logRuleAccess
	logStore  logRulePersistence
}

func Handler(store *Store, history *MetricsDB, health healthReporter, cfg configAccess, apply func(Config)) http.Handler {
	return HandlerWithAlerts(store, history, health, cfg, apply, alerts{})
}

// HandlerWithAlerts is Handler plus the rule endpoints.
func HandlerWithAlerts(store *Store, history *MetricsDB, health healthReporter, cfg configAccess, apply func(Config), al alerts) http.Handler {
	// Charts read stored history when there is any: it holds everything the live window
	// holds and more, so one source answers every range. Without it, ranges are capped at
	// whatever memory still has.
	chartsFrom := func(since time.Time) sampleSource {
		if history == nil {
			return store
		}
		return storedSamples{db: history, since: since, now: time.Now}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/metrics/latest", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, groupLatest(store.Latest()))
	})
	// Prometheus text exposition, so the same numbers the UI draws can be scraped by
	// Grafana, SigLens, SigNoz or anything else that speaks it, without FlatRun holding
	// the data hostage.
	mux.HandleFunc("/metrics/prometheus", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", prometheusContentType)
		_, _ = w.Write([]byte(renderPrometheus(store.Latest())))
	})
	mux.HandleFunc("/metrics/deployment", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		writeJSON(w, filterByDeployment(groupLatest(store.Latest()), name))
	})
	mux.HandleFunc("/metrics/timeseries", func(w http.ResponseWriter, r *http.Request) {
		deployment := r.URL.Query().Get("deployment")
		since := time.Now().Add(-15 * time.Minute)
		if v := r.URL.Query().Get("since"); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				since = time.Now().Add(-d)
			}
		}
		writeJSON(w, TimeSeriesResponse{Deployment: deployment, Metrics: buildTimeSeries(chartsFrom(since), deployment, since)})
	})
	mux.HandleFunc("/metrics/host", func(w http.ResponseWriter, r *http.Request) {
		since := time.Now().Add(-15 * time.Minute)
		if v := r.URL.Query().Get("since"); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				since = time.Now().Add(-d)
			}
		}
		all := buildTimeSeries(chartsFrom(since), "", since)
		host := make(map[string]MetricSeries, len(all))
		for metric, series := range all {
			if strings.HasPrefix(metric, "system.") {
				host[metric] = series
			}
		}
		writeJSON(w, TimeSeriesResponse{Metrics: host})
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if health == nil {
			writeJSON(w, []ContainerHealth{})
			return
		}
		writeJSON(w, filterHealth(health.Snapshot(), r.URL.Query().Get("deployment")))
	})
	mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		if cfg == nil {
			writeJSON(w, DefaultConfig())
			return
		}
		if r.Method == http.MethodPut {
			var incoming Config
			if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
				http.Error(w, "invalid config", http.StatusBadRequest)
				return
			}
			if err := cfg.Save(incoming); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if apply != nil {
				apply(cfg.Load())
			}
		}
		writeJSON(w, cfg.Load())
	})
	mux.HandleFunc("/alerts/rules", func(w http.ResponseWriter, r *http.Request) {
		if al.engine == nil || al.store == nil {
			writeJSON(w, []AlertRule{})
			return
		}
		if r.Method == http.MethodPut {
			var incoming []AlertRule
			if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
				http.Error(w, "invalid rules", http.StatusBadRequest)
				return
			}
			var err error
			incoming, err = mergeScoped(
				al.engine.Rules(), incoming, resourceAccess(r), alertRuleDeployment,
				func(rule AlertRule) string { return rule.ID },
			)
			if err != nil {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
			if err := al.store.Save(incoming); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			al.engine.SetRules(al.store.Load())
		}
		writeJSON(w, filterScoped(al.engine.Rules(), resourceAccess(r), alertRuleDeployment))
	})
	mux.HandleFunc("/alerts/log-rules", func(w http.ResponseWriter, r *http.Request) {
		if al.logEngine == nil || al.logStore == nil {
			writeJSON(w, []LogRule{})
			return
		}
		if r.Method == http.MethodPut {
			var incoming []LogRule
			if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
				http.Error(w, "invalid rules", http.StatusBadRequest)
				return
			}
			var err error
			incoming, err = mergeScoped(
				al.logEngine.Rules(), incoming, resourceAccess(r),
				func(rule LogRule) string { return rule.Deployment },
				func(rule LogRule) string { return rule.ID },
			)
			if err != nil {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
			if err := al.logStore.Save(incoming); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			al.logEngine.SetRules(al.logStore.Load())
		}
		writeJSON(w, filterScoped(
			al.logEngine.Rules(), resourceAccess(r), func(rule LogRule) string { return rule.Deployment },
		))
	})
	mux.HandleFunc("/alerts/incidents", func(w http.ResponseWriter, r *http.Request) {
		if al.logEngine == nil {
			writeJSON(w, []Incident{})
			return
		}
		incidents := filterScoped(
			al.logEngine.Incidents(), resourceAccess(r), func(incident Incident) string { return incident.Deployment },
		)
		if deployment := r.URL.Query().Get("deployment"); deployment != "" {
			filtered := make([]Incident, 0, len(incidents))
			for _, in := range incidents {
				if in.Deployment == deployment {
					filtered = append(filtered, in)
				}
			}
			incidents = filtered
		}
		writeJSON(w, incidents)
	})
	mux.HandleFunc("/alerts/responders", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, KnownResponders())
	})
	mux.HandleFunc("/alerts/firing", func(w http.ResponseWriter, r *http.Request) {
		if al.engine == nil {
			writeJSON(w, []AlertEvent{})
			return
		}
		writeJSON(w, filterScoped(
			al.engine.Firing(), resourceAccess(r), func(event AlertEvent) string { return event.Deployment },
		))
	})
	mux.HandleFunc("/alerts/events", func(w http.ResponseWriter, r *http.Request) {
		if al.engine == nil {
			writeJSON(w, []AlertEvent{})
			return
		}
		writeJSON(w, filterScoped(
			al.engine.Events(), resourceAccess(r), func(event AlertEvent) string { return event.Deployment },
		))
	})
	mux.HandleFunc("/health/events", func(w http.ResponseWriter, _ *http.Request) {
		if health == nil {
			writeJSON(w, []RecoveryEvent{})
			return
		}
		writeJSON(w, health.Events())
	})
	mux.HandleFunc("/metrics/series", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		key := SeriesKey{
			Deployment: q.Get("deployment"),
			Container:  q.Get("container"),
			Metric:     q.Get("metric"),
		}
		since := time.Time{}
		if v := q.Get("since"); v != "" {
			if secs, err := time.ParseDuration(v); err == nil {
				since = time.Now().Add(-secs)
			}
		}
		writeJSON(w, map[string]any{
			"deployment": key.Deployment,
			"container":  key.Container,
			"metric":     key.Metric,
			"samples":    store.Range(key, since),
		})
	})
	return mux
}

func resourceAccess(r *http.Request) pluginapi.ResourceAccess {
	value := r.Header.Get(pluginapi.ResourceAccessHeader)
	if value == "" {
		return pluginapi.ResourceAccess{Global: true}
	}
	access, err := pluginapi.DecodeResourceAccess(value)
	if err != nil {
		return pluginapi.ResourceAccess{}
	}
	return access
}

func alertRuleDeployment(rule AlertRule) string {
	switch rule.Metric {
	case MetricHostCPU, MetricHostMemUtil, MetricHostMemUsage, MetricHostMemLimit, MetricHostDisk:
		return ""
	default:
		return rule.Deployment
	}
}

func filterScoped[T any](items []T, access pluginapi.ResourceAccess, deployment func(T) string) []T {
	if access.Global {
		return items
	}
	filtered := make([]T, 0, len(items))
	for _, item := range items {
		if name := deployment(item); name != "" && access.Allows("deployment", name, "read") {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func mergeScoped[T any](stored, incoming []T, access pluginapi.ResourceAccess, deployment func(T) string, id func(T) string) ([]T, error) {
	if access.Global {
		return incoming, nil
	}
	merged := make([]T, 0, len(stored)+len(incoming))
	protectedIDs := make(map[string]struct{})
	for _, item := range stored {
		name := deployment(item)
		if name == "" || !access.Allows("deployment", name, "write") {
			merged = append(merged, item)
			if itemID := id(item); itemID != "" {
				protectedIDs[itemID] = struct{}{}
			}
		}
	}
	for _, item := range incoming {
		name := deployment(item)
		if name == "" || !access.Allows("deployment", name, "write") {
			unchanged := false
			for _, existing := range stored {
				if id(existing) == id(item) && reflect.DeepEqual(existing, item) {
					unchanged = true
					break
				}
			}
			if unchanged {
				continue
			}
			return nil, fmt.Errorf("no write access to alert scope")
		}
		if _, exists := protectedIDs[id(item)]; exists {
			return nil, fmt.Errorf("no write access to alert rule")
		}
		merged = append(merged, item)
	}
	return merged, nil
}

type containerMetrics struct {
	Container string             `json:"container"`
	Metrics   map[string]float64 `json:"metrics"`
	Updated   time.Time          `json:"updated"`
}

type deploymentMetrics struct {
	Deployment string             `json:"deployment"`
	Containers []containerMetrics `json:"containers"`
}

// groupLatest folds the flat latest-per-series list into deployment -> container -> metrics,
// the shape the UI renders as cards.
func groupLatest(points []LatestPoint) []deploymentMetrics {
	type ck struct{ dep, cont string }
	order := []ck{}
	byContainer := map[ck]*containerMetrics{}

	for _, p := range points {
		k := ck{p.Deployment, p.Container}
		cm := byContainer[k]
		if cm == nil {
			cm = &containerMetrics{Container: p.Container, Metrics: map[string]float64{}}
			byContainer[k] = cm
			order = append(order, k)
		}
		cm.Metrics[p.Metric] = p.Value
		if p.Time.After(cm.Updated) {
			cm.Updated = p.Time
		}
	}

	depOrder := []string{}
	byDeployment := map[string]*deploymentMetrics{}
	for _, k := range order {
		dm := byDeployment[k.dep]
		if dm == nil {
			dm = &deploymentMetrics{Deployment: k.dep}
			byDeployment[k.dep] = dm
			depOrder = append(depOrder, k.dep)
		}
		dm.Containers = append(dm.Containers, *byContainer[k])
	}

	out := make([]deploymentMetrics, 0, len(depOrder))
	for _, d := range depOrder {
		out = append(out, *byDeployment[d])
	}
	return out
}

func filterByDeployment(deps []deploymentMetrics, name string) []deploymentMetrics {
	if name == "" {
		return deps
	}
	out := make([]deploymentMetrics, 0, 1)
	for _, d := range deps {
		if d.Deployment == name {
			out = append(out, d)
		}
	}
	return out
}

func filterHealth(states []ContainerHealth, deployment string) []ContainerHealth {
	if deployment == "" {
		return states
	}
	out := make([]ContainerHealth, 0, len(states))
	for _, s := range states {
		if s.Deployment == deployment {
			out = append(out, s)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
