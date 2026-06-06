package api

import (
	"testing"

	"github.com/flatrun/agent/pkg/config"
)

func TestRuntimeConfigKeysAdvertisesTrustKeys(t *testing.T) {
	server := &Server{config: &config.Config{}}
	keys := server.runtimeConfigKeys()

	for _, key := range []string{"security.trusted_proxies", "security.trust_cf_header"} {
		if !keys[key] {
			t.Errorf("expected %q to be advertised as a runtime config key", key)
		}
	}
}

func TestTrustKeyApplierNoOpWhenSecurityDisabled(t *testing.T) {
	server := &Server{config: &config.Config{}}
	server.config.Security.Enabled = false

	apply := server.runtimeAppliers()["security.trusted_proxies"]
	if apply == nil {
		t.Fatal("expected an applier for security.trusted_proxies")
	}
	if err := apply(server); err != nil {
		t.Fatalf("applier should be a no-op when security is disabled, got: %v", err)
	}
}
