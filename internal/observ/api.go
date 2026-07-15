package observ

import (
	"encoding/json"
	"net/http"
	"time"
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
func Handler(store *Store, history *MetricsDB, health healthReporter, cfg configAccess, apply func(Config)) http.Handler {
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
