package runtimes

import (
	"crypto/sha256"
	"fmt"
)

const CachePath = "/flatrun-cache"
const CacheLabel = "dev.flatrun.cache"

func cacheName(scope, key string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + key))
	return fmt.Sprintf("flatrun-cache-%x", sum[:20])
}

func cacheEnvironment(original map[string]string) map[string]string {
	env := map[string]string{}
	for key, value := range original {
		env[key] = value
	}
	for key, value := range map[string]string{
		"GOCACHE": CachePath + "/go-build", "GOMODCACHE": CachePath + "/go-mod", "npm_config_cache": CachePath + "/npm",
		"npm_config_prefer_offline": "true",
		"RUNNER_TOOL_CACHE":         CachePath + "/tools", "AGENT_TOOLSDIRECTORY": CachePath + "/tools",
	} {
		env[key] = value
	}
	return env
}
