package routing

import (
	"context"
	"encoding/json"
	"testing"
)

type kubernetesRunnerStub struct {
	input []byte
	args  []string
}

func (r *kubernetesRunnerStub) Run(_ context.Context, input []byte, args ...string) ([]byte, error) {
	r.input = append([]byte(nil), input...)
	r.args = append([]string(nil), args...)
	return nil, nil
}

func TestK3sIngressRoutesThroughWorkloadService(t *testing.T) {
	runner := &kubernetesRunnerStub{}
	provider := NewK3sIngressProviderWithRunner("/etc/rancher/k3s.yaml", "apps", runner)
	err := provider.Reconcile(context.Background(), Route{
		ID: "shop", Service: "web", Domain: "shop.example.com", Protocol: "http",
		Backends: []Backend{{ID: "pod-one", Address: "10.42.0.8:8080", Healthy: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(runner.input, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["kind"] != "Ingress" || runner.args[0] != "--kubeconfig" || runner.args[4] != "apply" {
		t.Fatalf("manifest = %#v, args = %#v", manifest, runner.args)
	}
}
