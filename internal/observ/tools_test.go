package observ

import (
	"strings"
	"testing"
	"time"

	"github.com/flatrun/agent/pkg/pluginsdk"
)

func findTool(tools []pluginsdk.Tool, name string) (pluginsdk.Tool, bool) {
	for _, t := range tools {
		if t.Spec.Name == name {
			return t, true
		}
	}
	return pluginsdk.Tool{}, false
}

func newTools(t *testing.T) []pluginsdk.Tool {
	store := NewStore(10)
	watcher := NewHealthWatcher(func() ([]ContainerHealth, error) { return nil, nil }, func(string) error { return nil }, time.Second, time.Minute)
	return buildTools(store, watcher, NewConfigStore(t.TempDir()))
}

func TestRestartContainerRequiresScope(t *testing.T) {
	restart, ok := findTool(newTools(t), "restart_container")
	if !ok {
		t.Fatal("restart_container tool missing")
	}
	if !restart.Spec.Mutates {
		t.Error("restart_container must be marked as mutating")
	}
	// No scope (unscoped session) must be refused before any docker action.
	if _, err := restart.Run(map[string]any{"container": "x"}); err == nil || !strings.Contains(err.Error(), "deployment-scoped") {
		t.Errorf("expected scope-required error, got %v", err)
	}
}

func TestRestartContainerRefusesCrossDeployment(t *testing.T) {
	orig := containerProject
	defer func() { containerProject = orig }()
	// The container actually belongs to another deployment.
	containerProject = func(string) (string, error) { return "other-app", nil }

	restart, _ := findTool(newTools(t), "restart_container")
	_, err := restart.Run(map[string]any{"container": "victim", "_deployment": "myapp"})
	if err == nil || !strings.Contains(err.Error(), "not part of deployment") {
		t.Errorf("expected cross-deployment refusal, got %v", err)
	}
}

func TestHealthAndMetricsToolsAreReadOnly(t *testing.T) {
	tools := newTools(t)
	for _, name := range []string{"get_deployment_health", "get_deployment_metrics"} {
		tl, ok := findTool(tools, name)
		if !ok {
			t.Fatalf("%s missing", name)
		}
		if tl.Spec.Mutates {
			t.Errorf("%s should not be mutating", name)
		}
	}
}
