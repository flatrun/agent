package observ

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandlerLatestGroupsByDeployment(t *testing.T) {
	store := NewStore(10)
	now := time.Unix(1_700_000_000, 0)
	store.Record(ContainerSample{Deployment: "shop", Container: "shop-web", CPUPercent: 10, MemoryUsage: 200}, now)
	store.Record(ContainerSample{Deployment: "shop", Container: "shop-db", CPUPercent: 4, MemoryUsage: 500}, now)

	rec := httptest.NewRecorder()
	Handler(store, nil, nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics/latest", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got []deploymentMetrics
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if len(got) != 1 || got[0].Deployment != "shop" {
		t.Fatalf("expected one deployment 'shop', got %+v", got)
	}
	if len(got[0].Containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(got[0].Containers))
	}
	var web *containerMetrics
	for i := range got[0].Containers {
		if got[0].Containers[i].Container == "shop-web" {
			web = &got[0].Containers[i]
		}
	}
	if web == nil || web.Metrics[MetricCPUUsage] != 10 || web.Metrics[MetricMemoryUsage] != 200 {
		t.Errorf("shop-web container = %+v", web)
	}
}

func TestHandlerConfigRoundTrip(t *testing.T) {
	cfg := NewConfigStore(t.TempDir())
	var applied *Config
	h := Handler(NewStore(10), nil, cfg, func(c Config) { applied = &c })

	body := `{"sample_interval_seconds":10,"auto_restart":false,"restart_cooldown_seconds":300}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/config", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /config status = %d", rec.Code)
	}
	if applied == nil {
		t.Fatal("apply was not called on PUT /config")
	}
	if applied.AutoRestart != false {
		t.Errorf("apply got stale config: %+v", *applied)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/config", nil))
	var got Config
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SampleIntervalSeconds != 10 || got.AutoRestart != false || got.RestartCooldownSeconds != 300 {
		t.Errorf("config not persisted: %+v", got)
	}
}

func TestHandlerMetricsDeploymentFilters(t *testing.T) {
	store := NewStore(10)
	now := time.Unix(1_700_000_000, 0)
	store.Record(ContainerSample{Deployment: "shop", Container: "shop-web", CPUPercent: 5}, now)
	store.Record(ContainerSample{Deployment: "blog", Container: "blog-web", CPUPercent: 9}, now)

	rec := httptest.NewRecorder()
	Handler(store, nil, nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics/deployment?name=shop", nil))
	var got []deploymentMetrics
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Deployment != "shop" {
		t.Errorf("deployment filter = %+v, want only shop", got)
	}
}

func TestHandlerSeriesReturnsSamples(t *testing.T) {
	store := NewStore(10)
	base := time.Now().Add(-time.Minute)
	for i := 0; i < 3; i++ {
		store.Record(ContainerSample{Deployment: "d", Container: "c", CPUPercent: float64(i)}, base.Add(time.Duration(i)*time.Second))
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics/series?deployment=d&container=c&metric="+MetricCPUUsage+"&since=5m", nil)
	Handler(store, nil, nil, nil).ServeHTTP(rec, req)

	var got struct {
		Samples []Sample `json:"samples"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Samples) != 3 {
		t.Errorf("expected 3 samples, got %d", len(got.Samples))
	}
}
