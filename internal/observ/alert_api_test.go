package observ

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/pluginapi"
)

func alertHandler(t *testing.T) (http.Handler, *AlertEngine, *AlertStore) {
	t.Helper()
	store := NewStore(10)
	engine := NewAlertEngine(store)
	rules := NewAlertStore(t.TempDir())
	h := HandlerWithAlerts(store, nil, nil, nil, nil, alerts{engine: engine, store: rules})
	return h, engine, rules
}

func scopedAlertRequest(t *testing.T, method, path, body string, grants ...pluginapi.ResourceGrant) *http.Request {
	t.Helper()
	encoded, err := pluginapi.EncodeResourceAccess(pluginapi.ResourceAccess{Grants: grants})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(pluginapi.ResourceAccessHeader, encoded)
	return req
}

func TestAlertRulesAreScopedThroughTheHTTPAPI(t *testing.T) {
	metrics := NewStore(10)
	engine := NewAlertEngine(metrics)
	store := NewAlertStore(t.TempDir())
	if err := store.Save([]AlertRule{
		{ID: "shop", Name: "Shop CPU", Deployment: "shop", Metric: MetricCPUUsage, Comparison: ComparisonAbove, Threshold: 80, Enabled: true},
		{ID: "billing", Name: "Billing CPU", Deployment: "billing", Metric: MetricCPUUsage, Comparison: ComparisonAbove, Threshold: 80, Enabled: true},
		{ID: "host", Name: "Host CPU", Metric: MetricHostCPU, Comparison: ComparisonAbove, Threshold: 80, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	engine.SetRules(store.Load())
	h := HandlerWithAlerts(metrics, nil, nil, nil, nil, alerts{engine: engine, store: store})

	grant := pluginapi.ResourceGrant{Resource: "deployment", ID: "shop", Level: "write"}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, scopedAlertRequest(t, http.MethodGet, "/alerts/rules", "", grant))
	var visible []AlertRule
	if err := json.Unmarshal(rec.Body.Bytes(), &visible); err != nil {
		t.Fatal(err)
	}
	if len(visible) != 1 || visible[0].ID != "shop" {
		t.Fatalf("visible rules = %+v", visible)
	}

	body, _ := json.Marshal([]AlertRule{{ID: "shop", Name: "Shop memory", Deployment: "shop", Metric: MetricMemoryUsage, Comparison: ComparisonAbove, Threshold: 90, Enabled: true}})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, scopedAlertRequest(t, http.MethodPut, "/alerts/rules", string(body), grant))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	all := store.Load()
	if len(all) != 3 {
		t.Fatalf("stored rules = %+v", all)
	}
	for _, rule := range all {
		if rule.ID == "billing" && rule.Name != "Billing CPU" {
			t.Fatal("scoped update changed another deployment")
		}
		if rule.ID == "host" && rule.Name != "Host CPU" {
			t.Fatal("scoped update changed the host rule")
		}
	}
}

func TestAlertRulesRejectAnotherDeploymentThroughTheHTTPAPI(t *testing.T) {
	h, _, _ := alertHandler(t)
	grant := pluginapi.ResourceGrant{Resource: "deployment", ID: "shop", Level: "write"}
	body := `[{"name":"Billing CPU","deployment":"billing","metric":"container.cpu.usage","comparison":"above","threshold":80,"enabled":true}]`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, scopedAlertRequest(t, http.MethodPut, "/alerts/rules", body, grant))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

func TestAlertRulesRoundTripThroughTheAPI(t *testing.T) {
	h, engine, _ := alertHandler(t)

	body := `[{"name":"CPU high","metric":"container.cpu.usage","comparison":"above","threshold":80,"for_seconds":60,"enabled":true}]`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/alerts/rules", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var saved []AlertRule
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 {
		t.Fatalf("got %d rules, want 1", len(saved))
	}
	// A rule arrives without an id and must be given one, or it cannot be tracked across
	// evaluations.
	if saved[0].ID == "" {
		t.Error("saved rule has no id")
	}

	// The engine is what evaluates, so it has to have been handed the new rules.
	if got := engine.Rules(); len(got) != 1 || got[0].Name != "CPU high" {
		t.Errorf("engine rules = %+v", got)
	}
}

func TestContainerMemoryUtilizationRuleThroughTheAPI(t *testing.T) {
	h, engine, _ := alertHandler(t)
	body := `[{"name":"Memory high","metric":"container.memory.utilization","comparison":"above","threshold":90,"for_seconds":300,"enabled":true}]`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/alerts/rules", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	rules := engine.Rules()
	if len(rules) != 1 || rules[0].Metric != MetricMemoryUtilization || rules[0].Threshold != 90 {
		t.Errorf("engine rules = %+v", rules)
	}
}

func TestAlertRulesRejectAnUnusableRule(t *testing.T) {
	h, engine, _ := alertHandler(t)

	// A metric that is not collected can never fire, so it is refused rather than stored
	// and silently ignored.
	body := `[{"name":"Nonsense","metric":"container.disk.usage","comparison":"above","threshold":1,"enabled":true}]`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/alerts/rules", strings.NewReader(body)))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if len(engine.Rules()) != 0 {
		t.Error("an unusable rule reached the engine")
	}
}

func TestAlertRulesPersist(t *testing.T) {
	dir := t.TempDir()
	store := NewAlertStore(dir)

	if err := store.Save([]AlertRule{{
		Name: "CPU high", Metric: MetricCPUUsage, Comparison: ComparisonAbove, Threshold: 80, Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}

	// Rules are flat files, so a fresh reader sees what the last writer wrote.
	loaded := NewAlertStore(dir).Load()
	if len(loaded) != 1 || loaded[0].Name != "CPU high" || loaded[0].ID == "" {
		t.Errorf("rules did not survive: %+v", loaded)
	}
}

func TestAlertFiringEndpoint(t *testing.T) {
	store := NewStore(10)
	engine := NewAlertEngine(store)
	h := HandlerWithAlerts(store, nil, nil, nil, nil, alerts{engine: engine, store: NewAlertStore(t.TempDir())})

	engine.SetRules([]AlertRule{cpuRule(0)})
	record(store, 95, engine.now())
	engine.evaluate()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/alerts/firing", nil))

	var firing []AlertEvent
	if err := json.Unmarshal(rec.Body.Bytes(), &firing); err != nil {
		t.Fatal(err)
	}
	if len(firing) != 1 || firing[0].State != AlertFiring {
		t.Fatalf("firing = %+v", firing)
	}

	// Once it recovers it is no longer firing, so it drops off what needs attention.
	record(store, 5, engine.now())
	engine.evaluate()

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/alerts/firing", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &firing); err != nil {
		t.Fatal(err)
	}
	if len(firing) != 0 {
		t.Errorf("a recovered rule is still listed as firing: %+v", firing)
	}
}

func TestAlertEndpointsWithoutAlerting(t *testing.T) {
	// The plain Handler has no alerting wired; the endpoints must still answer rather than
	// panic on a nil engine.
	h := Handler(NewStore(10), nil, nil, nil, nil)
	for _, path := range []string{"/alerts/rules", "/alerts/firing", "/alerts/events"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d", path, rec.Code)
		}
	}
}
