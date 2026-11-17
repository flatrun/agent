package api

import (
	"crypto/sha256"
	"fmt"
	"github.com/flatrun/agent/internal/runs"
	"github.com/flatrun/agent/pkg/config"
)

func runnerSpec(subscription config.EventSubscription, repository, branch string) runs.Spec {
	r := subscription.GitHubRunner
	spec := runs.Spec{Runtime: r.Runtime, Image: r.Image, TimeoutSeconds: r.TimeoutSeconds, CPUs: r.CPUs, MemoryBytes: r.MemoryBytes, Egress: true, AllowPrivilegeEscalation: r.AllowPrivilegeEscalation}
	if r.Cache && repository != "" && branch != "" {
		sum := sha256.Sum256([]byte(subscription.Source + "\x00" + subscription.Name + "\x00" + repository + "\x00" + branch))
		spec.CacheKey = fmt.Sprintf("%x", sum[:])
	}
	return spec
}
