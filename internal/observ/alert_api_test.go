package observ

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func alertHandler(t *testing.T) (http.Handler, *AlertEngine, *AlertStore) {
	t.Helper()
	store := NewStore(10)
	engine := NewAlertEngine(store)
	rules := NewAlertStore(t.TempDir())
	h := HandlerWithAlerts(store, nil, nil, nil, nil, alerts{engine: engine, store: rules})
	return h, engine, rules
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
