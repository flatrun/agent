package system

import (
	"context"
	"testing"
	"time"
)

func TestGetNetworkInterfaces(t *testing.T) {
	ifaces := getNetworkInterfaces()

	// Should return at least something (even in CI there are usually non-loopback interfaces)
	// but we won't fail if empty since some environments are very minimal
	for _, iface := range ifaces {
		if iface.Name == "" {
			t.Error("Interface name should not be empty")
		}
		if len(iface.Addresses) == 0 {
			t.Errorf("Interface %s has no addresses", iface.Name)
		}
	}
}

func TestCheckResolverWithInvalidServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	check := checkResolver(ctx, "192.0.2.1") // RFC 5737 TEST-NET, won't respond
	if check.Server != "192.0.2.1" {
		t.Errorf("Server = %s, want 192.0.2.1", check.Server)
	}
	if check.Healthy {
		t.Error("Expected unhealthy for unreachable DNS server")
	}
	if check.Error == "" {
		t.Error("Expected error message for unreachable DNS server")
	}
}

func TestCheckDNSHealthStructure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	health := checkDNSHealth(ctx)

	if len(health.Resolvers) != len(defaultDNSServers) {
		t.Errorf("Expected %d resolver checks, got %d", len(defaultDNSServers), len(health.Resolvers))
	}

	for _, r := range health.Resolvers {
		if r.Server == "" {
			t.Error("Resolver server should not be empty")
		}
		if r.Latency < 0 {
			t.Errorf("Latency should be non-negative, got %d", r.Latency)
		}
	}
}

func TestNetworkHealthStructure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	health, err := CheckNetworkHealth(ctx)
	if err != nil {
		t.Fatalf("CheckNetworkHealth failed: %v", err)
	}

	if health.CheckedAt.IsZero() {
		t.Error("CheckedAt should not be zero")
	}

	if len(health.DNS.Resolvers) == 0 {
		t.Error("Expected at least one DNS resolver check")
	}
}

func TestServerInfoStructure(t *testing.T) {
	info, err := GetServerInfo()
	if err != nil {
		t.Fatalf("GetServerInfo failed: %v", err)
	}

	if info.Hostname == "" {
		t.Error("Hostname should not be empty")
	}
}
