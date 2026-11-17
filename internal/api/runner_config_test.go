package api

import (
	"github.com/flatrun/agent/pkg/config"
	"testing"
)

func TestRunnerCachesDoNotCrossRepositoryBranchOrSubscription(t *testing.T) {
	subscription := config.EventSubscription{Name: "runner", Source: "github/ci", GitHubRunner: config.GitHubRunnerSpec{Cache: true}}
	key := runnerSpec(subscription, "owner/repo", "main").CacheKey
	if key == "" {
		t.Fatal("cache not enabled")
	}
	for _, scope := range [][2]string{{"other/repo", "main"}, {"owner/repo", "feature"}, {"owner/repo", ""}} {
		if runnerSpec(subscription, scope[0], scope[1]).CacheKey == key {
			t.Fatal("cache scope collision")
		}
	}
	subscription.Name = "other-runner"
	if runnerSpec(subscription, "owner/repo", "main").CacheKey == key {
		t.Fatal("subscriptions share cache")
	}
	subscription.GitHubRunner.Cache = false
	if runnerSpec(subscription, "owner/repo", "main").CacheKey != "" {
		t.Fatal("cache is not opt-in")
	}
}
