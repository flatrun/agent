package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/sleep"
	"github.com/flatrun/agent/pkg/models"
)

func TestSleepSettings_Validation(t *testing.T) {
	s, r := newRedeployTestServer(t, &fakeRunner{})
	r.PUT("/api/deployments/:name/sleep", s.updateDeploymentSleep)
	if err := s.manager.CreateDeployment("app", "services:\n  app:\n    image: nginx\n", nil); err != nil {
		t.Fatal(err)
	}

	tooShort := httptest.NewRecorder()
	r.ServeHTTP(tooShort, httptest.NewRequest(http.MethodPut, "/api/deployments/app/sleep", strings.NewReader(`{"enabled":true,"idle_minutes":1}`)))
	if tooShort.Code != http.StatusBadRequest {
		t.Errorf("idle 1 minute = %d, want 400", tooShort.Code)
	}

	ok := httptest.NewRecorder()
	r.ServeHTTP(ok, httptest.NewRequest(http.MethodPut, "/api/deployments/app/sleep", strings.NewReader(`{"enabled":true,"idle_minutes":15}`)))
	if ok.Code != http.StatusOK {
		t.Fatalf("enable sleep = %d: %s", ok.Code, ok.Body.String())
	}
	d, _ := s.manager.GetDeployment("app")
	if d.Metadata.Sleep == nil || !d.Metadata.Sleep.Enabled || d.Metadata.Sleep.IdleMinutes != 15 {
		t.Errorf("sleep = %+v", d.Metadata.Sleep)
	}
}

func TestWake_OnlyWakesSleepingDeployments(t *testing.T) {
	runner := &fakeRunner{optsCh: make(chan actionOptions, 1)}
	s, r := newRedeployTestServer(t, runner)
	s.sleeper = sleep.NewController(time.Now().Add(-time.Hour))
	r.GET("/api/hooks/wake/:deployment", s.wakeDeployment)
	if err := s.manager.CreateDeployment("app", "services:\n  app:\n    image: nginx\n", nil); err != nil {
		t.Fatal(err)
	}
	d, _ := s.manager.GetDeployment("app")
	d.Metadata.Sleep = &models.SleepConfig{Enabled: true, IdleMinutes: 15}
	if err := s.manager.SaveMetadata("app", d.Metadata); err != nil {
		t.Fatal(err)
	}

	stopped := httptest.NewRecorder()
	r.ServeHTTP(stopped, httptest.NewRequest(http.MethodGet, "/api/hooks/wake/app", nil))
	if stopped.Code != http.StatusBadGateway || !strings.Contains(stopped.Body.String(), accessLogo) {
		t.Errorf("a deployment stopped by a person = %d, want a branded 502", stopped.Code)
	}

	if err := s.setSleeping("app", true); err != nil {
		t.Fatal(err)
	}
	asleep := httptest.NewRecorder()
	wake := httptest.NewRequest(http.MethodGet, "/api/hooks/wake/app", nil)
	wake.Header.Set("X-Original-Host", "app.example.com")
	r.ServeHTTP(asleep, wake)
	if asleep.Code != http.StatusServiceUnavailable || asleep.Header().Get("Retry-After") == "" {
		t.Errorf("sleeping deployment = %d: %s", asleep.Code, asleep.Body.String())
	}
	for _, expected := range []string{"Waking up", "<strong>app.example.com</strong>", accessLogo, `http-equiv="refresh"`} {
		if !strings.Contains(asleep.Body.String(), expected) {
			t.Errorf("wake page is missing %q", expected)
		}
	}
	select {
	case <-runner.optsCh:
	case <-time.After(5 * time.Second):
		t.Fatal("waking did not start the deployment")
	}
}

type staticActivity time.Time

func (a staticActivity) LastActive(context.Context, string, time.Time) (time.Time, error) {
	return time.Time(a), nil
}

func TestPutIdleDeploymentsToSleep_SkipsStopped(t *testing.T) {
	s, _ := newRedeployTestServer(t, &fakeRunner{})
	s.sleeper = sleep.NewController(time.Now().Add(-time.Hour), staticActivity(time.Now().Add(-time.Hour)))
	if err := s.manager.CreateDeployment("app", "services:\n  app:\n    image: nginx\n", nil); err != nil {
		t.Fatal(err)
	}
	d, _ := s.manager.GetDeployment("app")
	d.Metadata.Sleep = &models.SleepConfig{Enabled: true, IdleMinutes: 5}
	_ = s.manager.SaveMetadata("app", d.Metadata)

	// The deployment was never started, so there is nothing to put to sleep.
	if due := s.putIdleDeploymentsToSleep(context.Background(), time.Now()); len(due) != 0 {
		t.Errorf("stopped deployment was put to sleep: %v", due)
	}
}

func TestFailedSleepStopLeavesTheDeploymentAwake(t *testing.T) {
	s, _ := newRedeployTestServer(t, &fakeRunner{err: errors.New("stop failed")})
	if err := s.manager.CreateDeployment("app", "services:\n  app:\n    image: nginx\n", nil); err != nil {
		t.Fatal(err)
	}
	d, _ := s.manager.GetDeployment("app")
	d.Metadata.Sleep = &models.SleepConfig{Enabled: true, IdleMinutes: 5}
	if err := s.manager.SaveMetadata("app", d.Metadata); err != nil {
		t.Fatal(err)
	}
	if err := s.setSleeping("app", true); err != nil {
		t.Fatal(err)
	}

	job, created := s.startDeploymentJob("app", "stop", actionOptions{putToSleep: true})
	if !created {
		t.Fatal("stop job was not started")
	}
	if !waitFor(func() bool { return job.snapshot().Status == JobFailed }) {
		t.Fatalf("stop job = %v", job.snapshot().Status)
	}
	if !waitFor(func() bool {
		d, err := s.manager.GetDeployment("app")
		return err == nil && !d.Metadata.Sleep.Sleeping
	}) {
		t.Error("deployment is still marked sleeping after its stop failed")
	}
}
