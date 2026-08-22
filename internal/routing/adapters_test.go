package routing

import (
	"context"
	"strings"
	"testing"
)

type memoryConfigWriter struct {
	format  string
	content string
	removed string
}

func (w *memoryConfigWriter) Apply(_ context.Context, _ string, format string, content []byte) error {
	w.format = format
	w.content = string(content)
	return nil
}

func (w *memoryConfigWriter) Remove(_ context.Context, id string) error {
	w.removed = id
	return nil
}

func testRoute() Route {
	return Route{
		ID: "shop", Domain: "shop.example.com", Path: "/", Protocol: "http",
		Backends: []Backend{
			{ID: "replica-b", Address: "10.0.0.12:8080", Healthy: true, Weight: 1},
			{ID: "replica-a", Address: "10.0.0.11:8080", Healthy: true, Weight: 2},
		},
	}
}

func TestNginxProviderRendersWeightedUpstreamAndDrainsBackend(t *testing.T) {
	writer := &memoryConfigWriter{}
	provider := NewNginxProvider(writer)
	if err := provider.Reconcile(context.Background(), testRoute()); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	for _, expected := range []string{
		"upstream flatrun_shop",
		"server 10.0.0.11:8080 weight=2;",
		"server 10.0.0.12:8080 weight=1;",
		"proxy_pass http://flatrun_shop;",
	} {
		if !strings.Contains(writer.content, expected) {
			t.Fatalf("missing %q in:\n%s", expected, writer.content)
		}
	}
	if err := provider.Drain(context.Background(), "shop", "replica-b"); err != nil {
		t.Fatalf("Drain failed: %v", err)
	}
	if !strings.Contains(writer.content, "server 10.0.0.12:8080 weight=1 down;") {
		t.Fatalf("drained backend remains active:\n%s", writer.content)
	}
}

func TestNginxProviderOwnsReconciledBackendSlice(t *testing.T) {
	writer := &memoryConfigWriter{}
	provider := NewNginxProvider(writer)
	route := testRoute()
	if err := provider.Reconcile(context.Background(), route); err != nil {
		t.Fatal(err)
	}
	route.Backends[0].Healthy = false
	if err := provider.Drain(context.Background(), "shop", "replica-a"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(writer.content, "server 10.0.0.12:8080 weight=1 down;") {
		t.Fatalf("caller mutation changed the stored route:\n%s", writer.content)
	}
}

func TestTraefikProviderExcludesDrainedBackend(t *testing.T) {
	writer := &memoryConfigWriter{}
	provider := NewTraefikProvider(writer)
	if err := provider.Reconcile(context.Background(), testRoute()); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if writer.format != "traefik" || !strings.Contains(writer.content, "Host(`shop.example.com`) && PathPrefix(`/`)") {
		t.Fatalf("Traefik config:\n%s", writer.content)
	}
	if err := provider.Drain(context.Background(), "shop", "replica-a"); err != nil {
		t.Fatalf("Drain failed: %v", err)
	}
	if strings.Contains(writer.content, "10.0.0.11:8080") || !strings.Contains(writer.content, "10.0.0.12:8080") {
		t.Fatalf("Traefik drain config:\n%s", writer.content)
	}
}

func TestRoutingProvidersRejectUnsafeAddresses(t *testing.T) {
	route := testRoute()
	route.Backends[0].Address = "10.0.0.12:8080; include /etc/passwd"
	if err := NewNginxProvider(&memoryConfigWriter{}).Validate(context.Background(), route); err == nil {
		t.Fatal("invalid backend address should be rejected")
	}
}
