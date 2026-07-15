package observ

import (
	"testing"
	"time"
)

func TestHealthWatcherRestartsUnhealthy(t *testing.T) {
	states := []ContainerHealth{
		{Container: "shop-web", Deployment: "shop", Status: HealthUnhealthy},
		{Container: "shop-db", Deployment: "shop", Status: HealthHealthy},
	}
	var restarted []string
	w := NewHealthWatcher(
		func() ([]ContainerHealth, error) { return states, nil },
		func(c string) error { restarted = append(restarted, c); return nil },
		time.Second, time.Minute,
	)
	w.now = func() time.Time { return time.Unix(1_700_000_000, 0) }

	w.checkOnce()

	if len(restarted) != 1 || restarted[0] != "shop-web" {
		t.Fatalf("expected only shop-web restarted, got %v", restarted)
	}
	if evs := w.Events(); len(evs) != 1 || evs[0].Container != "shop-web" {
		t.Errorf("expected one recovery event for shop-web, got %+v", evs)
	}
}

func TestHealthWatcherHonorsCooldown(t *testing.T) {
	states := []ContainerHealth{{Container: "web", Deployment: "d", Status: HealthUnhealthy}}
	count := 0
	now := time.Unix(1_700_000_000, 0)
	w := NewHealthWatcher(
		func() ([]ContainerHealth, error) { return states, nil },
		func(string) error { count++; return nil },
		time.Second, 2*time.Minute,
	)
	w.now = func() time.Time { return now }

	w.checkOnce() // restarts
	w.checkOnce() // within cooldown, must not restart again
	if count != 1 {
		t.Fatalf("expected 1 restart within cooldown, got %d", count)
	}

	now = now.Add(3 * time.Minute) // past cooldown
	w.checkOnce()
	if count != 2 {
		t.Fatalf("expected a second restart after cooldown, got %d", count)
	}
}

func TestHealthWatcherNeverActsOnStoppedContainers(t *testing.T) {
	// A user-stopped container is not running, so it never appears in the source; the
	// watcher therefore cannot restart it.
	running := []ContainerHealth{{Container: "web", Deployment: "d", Status: HealthHealthy}}
	restarts := 0
	w := NewHealthWatcher(
		func() ([]ContainerHealth, error) { return running, nil },
		func(string) error { restarts++; return nil },
		time.Second, time.Minute,
	)
	w.checkOnce()
	if restarts != 0 {
		t.Errorf("no running container is unhealthy, expected 0 restarts, got %d", restarts)
	}
}

func TestHealthWatcherOnlyRestartsManaged(t *testing.T) {
	states := []ContainerHealth{
		{Container: "app-web", Deployment: "myapp", Status: HealthUnhealthy},
		{Container: "pagemind-cli-1", Deployment: "pagemind", Status: HealthUnhealthy},
	}
	var restarted []string
	w := NewHealthWatcher(
		func() ([]ContainerHealth, error) { return states, nil },
		func(c string) error { restarted = append(restarted, c); return nil },
		time.Second, time.Minute,
	)
	w.SetManaged(func(dep string) bool { return dep == "myapp" })

	w.checkOnce()
	if len(restarted) != 1 || restarted[0] != "app-web" {
		t.Fatalf("only the managed deployment should be restarted, got %v", restarted)
	}
}

func TestHealthWatcherGivesUpAfterMaxAttempts(t *testing.T) {
	states := []ContainerHealth{{Container: "web", Deployment: "d", Status: HealthUnhealthy}}
	count := 0
	now := time.Unix(1_700_000_000, 0)
	w := NewHealthWatcher(
		func() ([]ContainerHealth, error) { return states, nil },
		func(string) error { count++; return nil },
		time.Second, time.Minute,
	)
	w.now = func() time.Time { return now }

	// Each check is past the cooldown; it should restart only up to the cap.
	for i := 0; i < 6; i++ {
		w.checkOnce()
		now = now.Add(2 * time.Minute)
	}
	if count != maxRestartAttempts {
		t.Fatalf("expected at most %d restarts, got %d", maxRestartAttempts, count)
	}

	// Once healthy, the counter resets and it will act again if it fails later.
	states[0].Status = HealthHealthy
	w.checkOnce()
	states[0].Status = HealthUnhealthy
	now = now.Add(2 * time.Minute)
	w.checkOnce()
	if count != maxRestartAttempts+1 {
		t.Errorf("counter should reset after a healthy report, got %d restarts", count)
	}
}

func TestHealthWatcherReportsGivingUp(t *testing.T) {
	states := []ContainerHealth{{Container: "web", Deployment: "d", Status: HealthUnhealthy}}
	now := time.Unix(1_700_000_000, 0)
	w := NewHealthWatcher(
		func() ([]ContainerHealth, error) { return states, nil },
		func(string) error { return nil },
		time.Second, time.Minute,
	)
	w.now = func() time.Time { return now }

	exhausted := make(chan ExhaustedEvent, 10)
	w.OnExhausted(func(ev ExhaustedEvent) { exhausted <- ev })

	// Keep checking well past the restart cap. The container never recovers, so the
	// watcher stops acting and has to say so.
	for i := 0; i < 8; i++ {
		w.checkOnce()
		now = now.Add(2 * time.Minute)
	}

	select {
	case ev := <-exhausted:
		if ev.Container != "web" || ev.Deployment != "d" {
			t.Errorf("event = %+v, want web in d", ev)
		}
		if ev.Attempts != maxRestartAttempts {
			t.Errorf("attempts = %d, want %d", ev.Attempts, maxRestartAttempts)
		}
	case <-time.After(time.Second):
		t.Fatal("auto-restart gave up without reporting it")
	}

	// Checks run every few seconds for as long as it stays unhealthy, so it must be
	// reported once per streak, not once per check.
	select {
	case ev := <-exhausted:
		t.Errorf("reported giving up more than once: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}

	// Recovering and failing again is a new streak, and worth hearing about.
	states[0].Status = HealthHealthy
	w.checkOnce()
	states[0].Status = HealthUnhealthy
	for i := 0; i < 8; i++ {
		now = now.Add(2 * time.Minute)
		w.checkOnce()
	}

	select {
	case <-exhausted:
	case <-time.After(time.Second):
		t.Error("a second streak that exhausts its restarts was not reported")
	}
}

func TestHealthWatcherFiresOnRecover(t *testing.T) {
	states := []ContainerHealth{{Container: "web", Deployment: "shop", Status: HealthUnhealthy}}
	w := NewHealthWatcher(
		func() ([]ContainerHealth, error) { return states, nil },
		func(string) error { return nil },
		time.Second, time.Minute,
	)
	got := make(chan RecoveryEvent, 1)
	w.OnRecover(func(ev RecoveryEvent) { got <- ev })

	w.checkOnce()
	select {
	case ev := <-got:
		if ev.Container != "web" || ev.Deployment != "shop" {
			t.Errorf("unexpected recover event %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("OnRecover was not fired on restart")
	}
}

func TestHealthWatcherDisabledObservesButDoesNotRestart(t *testing.T) {
	states := []ContainerHealth{{Container: "web", Deployment: "d", Status: HealthUnhealthy}}
	restarts := 0
	w := NewHealthWatcher(
		func() ([]ContainerHealth, error) { return states, nil },
		func(string) error { restarts++; return nil },
		time.Second, time.Minute,
	)
	w.SetEnabled(false)
	w.checkOnce()

	if restarts != 0 {
		t.Errorf("disabled watcher must not restart, got %d", restarts)
	}
	if snap := w.Snapshot(); len(snap) != 1 || snap[0].Status != HealthUnhealthy {
		t.Errorf("disabled watcher should still observe health, got %+v", snap)
	}
}

func TestHealthFromStatus(t *testing.T) {
	cases := map[string]string{
		"Up 2 hours (healthy)":            HealthHealthy,
		"Up 5 minutes (unhealthy)":        HealthUnhealthy,
		"Up 3 seconds (health: starting)": HealthStarting,
		"Up 10 days":                      HealthNone,
	}
	for in, want := range cases {
		if got := healthFromStatus(in); got != want {
			t.Errorf("healthFromStatus(%q) = %q, want %q", in, got, want)
		}
	}
}
