package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/sleep"
	"github.com/flatrun/agent/pkg/models"
)

const shopCompose = `services:
  web:
    image: nginx
    depends_on: [api]
  api:
    image: nginx
    depends_on: [db]
  db:
    image: postgres
  worker:
    image: busybox
    depends_on: [db]
`

func serviceNames(targets []sleep.Target) []string {
	var names []string
	for _, t := range targets {
		names = append(names, t.Name)
	}
	slices.Sort(names)
	return names
}

func shopDeployment(running ...string) models.Deployment {
	d := models.Deployment{Name: "shop", Metadata: &models.ServiceMetadata{ServiceSleep: map[string]models.SleepConfig{
		"worker": {Enabled: true, IdleMinutes: 10},
		"db":     {Enabled: true, IdleMinutes: 10},
	}}}
	for _, svc := range []models.Service{
		{Name: "web", DependsOn: []string{"api"}},
		{Name: "api", DependsOn: []string{"db"}},
		{Name: "db"},
		{Name: "worker", DependsOn: []string{"db"}},
	} {
		if slices.Contains(running, svc.Name) {
			svc.RunningReplicas = 1
		}
		d.Services = append(d.Services, svc)
	}
	return d
}

func TestAServiceSleepsOnlyWhenNothingAwakeNeedsIt(t *testing.T) {
	d := shopDeployment("web", "api", "db", "worker")
	if got := serviceNames(serviceSleepTargets(d)); !slices.Equal(got, []string{"shop/worker"}) {
		t.Fatalf("targets = %v, want only the worker: api and worker still use db", got)
	}

	d = shopDeployment("db")
	if got := serviceNames(serviceSleepTargets(d)); !slices.Equal(got, []string{"shop/db"}) {
		t.Fatalf("targets = %v, want db once nothing running depends on it", got)
	}

	d = shopDeployment("worker", "db")
	d.Metadata.ServiceSleep["worker"] = models.SleepConfig{Enabled: true, IdleMinutes: 10, Sleeping: true}
	if got := serviceNames(serviceSleepTargets(d)); !slices.Equal(got, []string{"shop/db"}) {
		t.Fatalf("targets = %v, want db: its only dependent is asleep", got)
	}

	d = shopDeployment("web", "api", "db", "worker")
	d.Metadata.Sleep = &models.SleepConfig{Enabled: true, IdleMinutes: 15}
	if got := serviceSleepTargets(d); len(got) != 0 {
		t.Fatalf("targets = %v, want none while the whole deployment sleeps", got)
	}
}

func TestWakeOrderStartsDependenciesFirst(t *testing.T) {
	d := shopDeployment()
	if got := wakeOrder(&d, "web"); !slices.Equal(got, []string{"db", "api", "web"}) {
		t.Fatalf("wake order = %v", got)
	}
}

type recordingRunner struct {
	mu      sync.Mutex
	started []string
	err     error
}

func (r *recordingRunner) run(action, _, service string, _ actionOptions, _ func(string)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, action+" "+service)
	return r.err
}

func (r *recordingRunner) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.started...)
}

func TestWakingAServiceStartsItsStoppedDependencies(t *testing.T) {
	s, r := newRedeployTestServer(t, &fakeRunner{})
	runner := &recordingRunner{}
	s.runServiceAction = runner.run
	s.sleeper = sleep.NewController(time.Now().Add(-time.Hour))
	r.GET("/api/hooks/wake/:deployment/:service", s.wakeService)
	if err := s.manager.CreateDeployment("shop", shopCompose, nil); err != nil {
		t.Fatal(err)
	}
	d, _ := s.manager.GetDeployment("shop")
	d.Metadata.ServiceSleep = map[string]models.SleepConfig{"web": {Enabled: true, IdleMinutes: 10}}
	if err := s.manager.SaveMetadata("shop", d.Metadata); err != nil {
		t.Fatal(err)
	}

	stopped := httptest.NewRecorder()
	r.ServeHTTP(stopped, httptest.NewRequest(http.MethodGet, "/api/hooks/wake/shop/web", nil))
	if stopped.Code != http.StatusBadGateway {
		t.Fatalf("a service stopped by a person = %d, want 502", stopped.Code)
	}

	if err := s.setServiceSleeping("shop", "web", true); err != nil {
		t.Fatal(err)
	}
	asleep := httptest.NewRecorder()
	r.ServeHTTP(asleep, httptest.NewRequest(http.MethodGet, "/api/hooks/wake/shop/web", nil))
	if asleep.Code != http.StatusServiceUnavailable || !strings.Contains(asleep.Body.String(), "Waking up") {
		t.Fatalf("sleeping service = %d: %s", asleep.Code, asleep.Body.String())
	}
	if !waitFor(func() bool { return len(runner.calls()) == 3 }) {
		t.Fatalf("started = %v", runner.calls())
	}
	if got := runner.calls(); !slices.Equal(got, []string{"start db", "start api", "start web"}) {
		t.Fatalf("started = %v, want dependencies first", got)
	}
}

func TestFailedServiceSleepStopLeavesTheServiceAwake(t *testing.T) {
	s, _ := newRedeployTestServer(t, &fakeRunner{err: errors.New("stop failed")})
	if err := s.manager.CreateDeployment("shop", shopCompose, nil); err != nil {
		t.Fatal(err)
	}
	d, _ := s.manager.GetDeployment("shop")
	d.Metadata.ServiceSleep = map[string]models.SleepConfig{"worker": {Enabled: true, IdleMinutes: 10}}
	if err := s.manager.SaveMetadata("shop", d.Metadata); err != nil {
		t.Fatal(err)
	}

	s.putServiceToSleep("shop", "worker")
	if !waitFor(func() bool {
		d, err := s.manager.GetDeployment("shop")
		return err == nil && !d.Metadata.ServiceSleep["worker"].Sleeping && s.jobs.activeFor("shop") == nil
	}) {
		t.Fatal("worker is still marked sleeping after its stop failed")
	}
}

func TestServiceSleepSettings(t *testing.T) {
	s, r := newRedeployTestServer(t, &fakeRunner{})
	r.PUT("/api/deployments/:name/services/:service/sleep", s.updateServiceSleep)
	if err := s.manager.CreateDeployment("shop", shopCompose, nil); err != nil {
		t.Fatal(err)
	}

	put := func(service, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/deployments/shop/services/"+service+"/sleep", strings.NewReader(body)))
		return rec
	}
	if rec := put("worker", `{"enabled":true,"idle_minutes":1}`); rec.Code != http.StatusBadRequest {
		t.Errorf("idle time under 5 minutes = %d", rec.Code)
	}
	if rec := put("ghost", `{"enabled":true,"idle_minutes":10}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown service = %d", rec.Code)
	}
	rec := put("worker", `{"enabled":true,"idle_minutes":10}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"active":true`) {
		t.Fatalf("enable = %d: %s", rec.Code, rec.Body.String())
	}
	d, _ := s.manager.GetDeployment("shop")
	if cfg := d.Metadata.ServiceSleep["worker"]; !cfg.Enabled || cfg.IdleMinutes != 10 {
		t.Fatalf("saved = %+v", cfg)
	}

	d.Metadata.Sleep = &models.SleepConfig{Enabled: true, IdleMinutes: 15}
	_ = s.manager.SaveMetadata("shop", d.Metadata)
	if rec := put("worker", `{"enabled":true,"idle_minutes":20}`); !strings.Contains(rec.Body.String(), `"active":false`) {
		t.Errorf("while the whole deployment sleeps the service setting must report inactive: %s", rec.Body.String())
	}

	if rec := put("worker", `{"enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("disable = %d", rec.Code)
	}
	d, _ = s.manager.GetDeployment("shop")
	if _, ok := d.Metadata.ServiceSleep["worker"]; ok {
		t.Fatal("a disabled service must be removed from service_sleep")
	}
}

func TestTurningSleepOffStartsAServiceThatIsAsleep(t *testing.T) {
	s, r := newRedeployTestServer(t, &fakeRunner{})
	runner := &recordingRunner{}
	s.runServiceAction = runner.run
	r.PUT("/api/deployments/:name/services/:service/sleep", s.updateServiceSleep)
	if err := s.manager.CreateDeployment("shop", shopCompose, nil); err != nil {
		t.Fatal(err)
	}
	d, _ := s.manager.GetDeployment("shop")
	d.Metadata.ServiceSleep = map[string]models.SleepConfig{"worker": {Enabled: true, IdleMinutes: 10, Sleeping: true}}
	if err := s.manager.SaveMetadata("shop", d.Metadata); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/deployments/shop/services/worker/sleep", strings.NewReader(`{"enabled":false}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("disable = %d: %s", rec.Code, rec.Body.String())
	}
	if !waitFor(func() bool { return slices.Contains(runner.calls(), "start worker") }) {
		t.Fatalf("started = %v, want the sleeping worker started", runner.calls())
	}
}

func TestSleepCannotBeTurnedOffWhileAnActionHoldsTheService(t *testing.T) {
	for _, held := range []struct {
		action string
		opts   actionOptions
	}{
		{"stop", actionOptions{putToSleep: true}},
		{"start", actionOptions{}},
	} {
		t.Run(held.action, func(t *testing.T) {
			assertSleepOffRefusedWhileHeld(t, held.action, held.opts)
		})
	}
}

func assertSleepOffRefusedWhileHeld(t *testing.T, action string, opts actionOptions) {
	t.Helper()
	s, r := newRedeployTestServer(t, &fakeRunner{})
	r.PUT("/api/deployments/:name/services/:service/sleep", s.updateServiceSleep)
	if err := s.manager.CreateDeployment("shop", shopCompose, nil); err != nil {
		t.Fatal(err)
	}
	d, _ := s.manager.GetDeployment("shop")
	d.Metadata.ServiceSleep = map[string]models.SleepConfig{"worker": {Enabled: true, IdleMinutes: 10, Sleeping: true}}
	if err := s.manager.SaveMetadata("shop", d.Metadata); err != nil {
		t.Fatal(err)
	}
	if _, created := s.jobs.createScoped("shop", "worker", action, opts); !created {
		t.Fatalf("could not hold the service with a %s", action)
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/deployments/shop/services/worker/sleep", strings.NewReader(`{"enabled":false}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("disable during the %s = %d: %s", action, rec.Code, rec.Body.String())
	}
	d, _ = s.manager.GetDeployment("shop")
	if !d.Metadata.ServiceSleepEnabled("worker") {
		t.Fatal("the setting must stay so the wake route can still start the worker")
	}
}

func TestStartingAServiceByHandStartsItsSleepingDependencies(t *testing.T) {
	s, r := newRedeployTestServer(t, &fakeRunner{})
	runner := &recordingRunner{}
	s.runServiceAction = runner.run
	r.POST("/api/deployments/:name/services/:service/job", s.enqueueServiceJob)
	if err := s.manager.CreateDeployment("shop", shopCompose, nil); err != nil {
		t.Fatal(err)
	}
	d, _ := s.manager.GetDeployment("shop")
	d.Metadata.ServiceSleep = map[string]models.SleepConfig{"db": {Enabled: true, IdleMinutes: 10, Sleeping: true}}
	if err := s.manager.SaveMetadata("shop", d.Metadata); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/deployments/shop/services/api/job", strings.NewReader(`{"action":"start"}`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !waitFor(func() bool { return len(runner.calls()) == 2 }) {
		t.Fatalf("started = %v", runner.calls())
	}
	if got := runner.calls(); !slices.Equal(got, []string{"start db", "start api"}) {
		t.Fatalf("started = %v, want the sleeping database first", got)
	}
}

func TestServiceSleepIsNotSavedWhenNginxRejectsTheWakeRoute(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	s.config.Nginx.Enabled = true
	createTestDeployment(t, tmpDir, "shop", &models.ServiceMetadata{
		Name:    "shop",
		Type:    "web",
		Domains: []models.DomainConfig{{ID: "site", Service: "web", ContainerPort: 80, Domain: "shop.example.com"}},
	})

	resp, body := doJSON(t, http.MethodPut, ts.URL+"/api/deployments/shop/services/web/sleep",
		map[string]any{"enabled": true, "idle_minutes": 10})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d: %v", resp.StatusCode, body)
	}
	d, _ := s.manager.GetDeployment("shop")
	if d.Metadata.ServiceSleepEnabled("web") {
		t.Fatal("sleep saved although nginx has no route to wake the service")
	}
}
