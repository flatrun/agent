package observ

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Health status values as reported by Docker's HEALTHCHECK.
const (
	HealthHealthy   = "healthy"
	HealthUnhealthy = "unhealthy"
	HealthStarting  = "starting"
	HealthNone      = "none"
)

// ContainerHealth is a container's current health, tagged with its deployment.
type ContainerHealth struct {
	Container  string `json:"container"`
	Deployment string `json:"deployment"`
	Status     string `json:"status"`
}

// HealthSource returns the current health of running containers. Injectable for tests.
type HealthSource func() ([]ContainerHealth, error)

// RestartFunc restarts a container by name. Injectable for tests.
type RestartFunc func(container string) error

// RecoveryEvent records a self-heal action for the UI/audit.
type RecoveryEvent struct {
	Container  string    `json:"container"`
	Deployment string    `json:"deployment"`
	At         time.Time `json:"at"`
}

// maxRestartAttempts bounds how many times a container is auto-restarted while it stays
// unhealthy, so a container a restart cannot fix is not restarted forever. The counter resets
// once the container reports healthy again.
const maxRestartAttempts = 3

// maxRecoveryEvents bounds the retained recovery history so a container that flaps for the
// life of the process cannot grow the slice without limit; only the most recent are kept.
const maxRecoveryEvents = 500

// ExhaustedEvent reports that auto-restart has stopped trying on a container. Restarting it
// did not fix it, so it stays unhealthy until someone intervenes.
type ExhaustedEvent struct {
	Container  string    `json:"container"`
	Deployment string    `json:"deployment"`
	Attempts   int       `json:"attempts"`
	At         time.Time `json:"at"`
}

// HealthWatcher restarts running-but-unhealthy containers. It only acts on running containers
// (so it never revives a deployment the user intentionally stopped), only on deployments
// FlatRun manages, and only up to a bounded number of attempts per unhealthy streak.
type HealthWatcher struct {
	source   HealthSource
	restart  RestartFunc
	interval time.Duration
	cooldown time.Duration
	now      func() time.Time

	mu          sync.Mutex
	enabled     bool
	managed     func(deployment string) bool
	onRecover   func(RecoveryEvent)
	onExhausted func(ExhaustedEvent)
	health      map[string]ContainerHealth
	lastHeal    map[string]time.Time
	attempts    map[string]int
	// exhausted marks containers already reported as beyond auto-restart, so a container
	// that stays unhealthy is reported once per streak rather than on every check.
	exhausted map[string]bool
	events    []RecoveryEvent
}

// OnExhausted registers a callback fired once when auto-restart gives up on a container,
// which is the point the watcher stops acting and an operator has to.
func (w *HealthWatcher) OnExhausted(fn func(ExhaustedEvent)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onExhausted = fn
}

// SetManaged restricts auto-restart to deployments for which the predicate returns true.
// Health is still observed for all containers; only the restart action is scoped.
func (w *HealthWatcher) SetManaged(fn func(deployment string) bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.managed = fn
}

// OnRecover registers a callback fired after each auto-restart, so the core notification
// service can be told a container was recovered.
func (w *HealthWatcher) OnRecover(fn func(RecoveryEvent)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onRecover = fn
}

// SetEnabled turns auto-restart on or off. Health is still observed either way.
func (w *HealthWatcher) SetEnabled(on bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.enabled = on
}

func NewHealthWatcher(source HealthSource, restart RestartFunc, interval, cooldown time.Duration) *HealthWatcher {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if cooldown <= 0 {
		cooldown = 2 * time.Minute
	}
	return &HealthWatcher{
		source:    source,
		restart:   restart,
		interval:  interval,
		cooldown:  cooldown,
		now:       time.Now,
		enabled:   true,
		health:    map[string]ContainerHealth{},
		lastHeal:  map[string]time.Time{},
		attempts:  map[string]int{},
		exhausted: map[string]bool{},
	}
}

// checkOnce reads health once and restarts any unhealthy container past its cooldown.
func (w *HealthWatcher) checkOnce() {
	states, err := w.source()
	if err != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	w.health = make(map[string]ContainerHealth, len(states))
	for _, s := range states {
		w.health[s.Container] = s
		if s.Status == HealthHealthy {
			delete(w.attempts, s.Container)
			delete(w.lastHeal, s.Container)
			delete(w.exhausted, s.Container)
		}
		if !w.enabled || s.Status != HealthUnhealthy {
			continue
		}
		// Only act on deployments FlatRun manages, never arbitrary host containers.
		if w.managed != nil && !w.managed(s.Deployment) {
			continue
		}
		// Stop once repeated restarts have not fixed it; the container stays flagged
		// unhealthy for the operator instead of being restarted forever. Say so once:
		// from here nothing else will act on it, and silence would read as health.
		if w.attempts[s.Container] >= maxRestartAttempts {
			if !w.exhausted[s.Container] {
				w.exhausted[s.Container] = true
				if w.onExhausted != nil {
					go w.onExhausted(ExhaustedEvent{
						Container:  s.Container,
						Deployment: s.Deployment,
						Attempts:   w.attempts[s.Container],
						At:         w.now(),
					})
				}
			}
			continue
		}
		last, seen := w.lastHeal[s.Container]
		if seen && w.now().Sub(last) < w.cooldown {
			continue
		}
		if err := w.restart(s.Container); err != nil {
			continue
		}
		w.lastHeal[s.Container] = w.now()
		w.attempts[s.Container]++
		ev := RecoveryEvent{Container: s.Container, Deployment: s.Deployment, At: w.now()}
		w.events = append(w.events, ev)
		if len(w.events) > maxRecoveryEvents {
			w.events = w.events[len(w.events)-maxRecoveryEvents:]
		}
		if w.onRecover != nil {
			go w.onRecover(ev)
		}
	}
}

// Run checks health on each tick until ctx is cancelled.
func (w *HealthWatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	w.checkOnce()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.checkOnce()
		}
	}
}

// Snapshot returns the last-seen health per container.
func (w *HealthWatcher) Snapshot() []ContainerHealth {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]ContainerHealth, 0, len(w.health))
	for _, h := range w.health {
		out = append(out, h)
	}
	return out
}

// Events returns the recovery actions taken, most recent last. Always non-nil so it
// serializes to a JSON array rather than null.
func (w *HealthWatcher) Events() []RecoveryEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]RecoveryEvent, 0, len(w.events))
	return append(out, w.events...)
}

// DockerHealthSource reads container health via `docker ps`.
func DockerHealthSource() ([]ContainerHealth, error) {
	deployments := containerDeployments()
	out, err := exec.Command("docker", "ps", "--format", "{{json .}}").Output()
	if err != nil {
		return nil, err
	}
	var states []ContainerHealth
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var raw struct {
			Names  string `json:"Names"`
			Status string `json:"Status"`
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		states = append(states, ContainerHealth{
			Container:  raw.Names,
			Deployment: deployments[raw.Names],
			Status:     healthFromStatus(raw.Status),
		})
	}
	return states, nil
}

// healthFromStatus reads the "(healthy)" / "(unhealthy)" / "(health: starting)" suffix
// Docker appends to a running container's Status string.
func healthFromStatus(status string) string {
	switch {
	case strings.Contains(status, "(unhealthy)"):
		return HealthUnhealthy
	case strings.Contains(status, "(healthy)"):
		return HealthHealthy
	case strings.Contains(status, "health: starting"):
		return HealthStarting
	default:
		return HealthNone
	}
}

// DockerRestart restarts a container by name.
func DockerRestart(container string) error {
	return exec.Command("docker", "restart", container).Run()
}
