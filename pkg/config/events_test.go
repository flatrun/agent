package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEventsConfigLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(`events:
  endpoints:
    - name: ci
      provider: github
      secret: your-webhook-secret-here
  github_app:
    app_id: 42
    private_key_file: /etc/flatrun/github.pem
  subscriptions:
    - name: runner
      source: github/ci
      action: github_runner
      labels: [self-hosted, flatrun]
      github_runner:
        timeout_seconds: 120
`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Events.Subscriptions[0]
	if s.GitHubRunner.Runtime != "firecracker" || s.GitHubRunner.Image != "ghcr.io/actions/actions-runner" || s.GitHubRunner.RunnerGroupID != 1 || s.GitHubRunner.TimeoutSeconds != 120 || cfg.Events.DeliveryLimit != 1000 {
		t.Fatalf("defaults: %+v", cfg.Events)
	}
	entry, err := Get(cfg, "events.endpoints")
	if err != nil || entry.Value != nil {
		t.Fatal("endpoint secrets visible")
	}
	if Set(cfg, "events", nil) == nil {
		t.Fatal("events section writable through API")
	}
	if Set(cfg, "events.subscriptions", nil) == nil {
		t.Fatal("events config writable through API")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(raw), "      labels: [self-hosted, flatrun]\n", "")), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "at least one label") {
		t.Fatalf("unlabelled runner accepted: %v", err)
	}

	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(raw), "      github_runner:\n", "      github_runner:\n        runtime: docker\n")), 0600); err != nil {
		t.Fatal(err)
	}
	explicit, err := Load(path)
	if err != nil || explicit.Events.Subscriptions[0].GitHubRunner.Runtime != "docker" {
		t.Fatalf("explicit Docker runtime changed: %v", err)
	}
	for _, kind := range []string{"github.workflow_job.queued", "github.workflow_job.completed"} {
		cfg.Events.Subscriptions[0].Type = kind
		if err := cfg.Events.Validate(); err == nil || !strings.Contains(err.Error(), "event type is implicit") {
			t.Fatalf("runner accepted explicit type %q: %v", kind, err)
		}
	}
}
