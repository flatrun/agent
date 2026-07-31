package observ

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// DeploymentRestartFunc restarts a deployment given its directory.
type DeploymentRestartFunc func(dir string) error

// DockerComposeRestart restarts every service of a deployment by running compose
// in its directory. It restarts the deployment rather than a single container,
// which is what an operator means by "restart it".
func DockerComposeRestart(dir string) error {
	cmd := exec.Command("docker", "compose", "restart")
	cmd.Dir = dir
	return cmd.Run()
}

// ActionRunner carries out a firing rule's action. It mirrors the health
// watcher's guardrails so a metric alert cannot turn into a restart loop: only
// FlatRun-managed deployments are touched, and a deployment is not restarted
// again until the cooldown has passed.
type ActionRunner struct {
	restart  DeploymentRestartFunc
	managed  func(deployment string) bool
	cooldown time.Duration
	dataDir  string
	now      func() time.Time

	mu   sync.Mutex
	last map[string]time.Time
}

func NewActionRunner(restart DeploymentRestartFunc, managed func(string) bool, cooldown time.Duration, dataDir string) *ActionRunner {
	if cooldown <= 0 {
		cooldown = 5 * time.Minute
	}
	return &ActionRunner{
		restart:  restart,
		managed:  managed,
		cooldown: cooldown,
		dataDir:  dataDir,
		now:      time.Now,
		last:     map[string]time.Time{},
	}
}

// Run executes the event's action and returns a short message describing what it
// did, or "" when it did nothing (not a restart action, no deployment, not
// managed, still cooling down, or the restart failed).
func (a *ActionRunner) Run(ev AlertEvent) string {
	if ev.Action != ActionRestart || ev.Deployment == "" {
		return ""
	}
	if a.managed != nil && !a.managed(ev.Deployment) {
		return ""
	}

	a.mu.Lock()
	if last, ok := a.last[ev.Deployment]; ok && a.now().Sub(last) < a.cooldown {
		a.mu.Unlock()
		return ""
	}
	a.last[ev.Deployment] = a.now()
	a.mu.Unlock()

	if err := a.restart(filepath.Join(a.dataDir, ev.Deployment)); err != nil {
		return ""
	}
	return fmt.Sprintf("Restarted %s after %s fired.", ev.Deployment, ev.RuleName)
}
