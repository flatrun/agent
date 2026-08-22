package orchestrator

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

type kubectlCall struct {
	input []byte
	args  []string
}

type fakeKubectl struct {
	calls     []kubectlCall
	responses [][]byte
}

func (f *fakeKubectl) Run(_ context.Context, input []byte, args ...string) ([]byte, error) {
	f.calls = append(f.calls, kubectlCall{input: input, args: append([]string(nil), args...)})
	if len(f.responses) == 0 {
		return nil, nil
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response, nil
}

func TestK3sApplyUsesConfiguredClusterAndNamespace(t *testing.T) {
	runner := &fakeKubectl{responses: [][]byte{
		nil,
		[]byte(`{"metadata":{"labels":{"flatrun.port":"8080"}},"spec":{"replicas":2},"status":{"availableReplicas":2}}`),
		[]byte(`{"items":[]}`),
	}}
	provider := NewK3sProvider("/etc/rancher/k3s.yaml", "apps")
	provider.runner = runner

	status, err := provider.Apply(context.Background(), Workload{ID: "shop", Image: "shop:1", Port: 8080, Replicas: 2, Environment: map[string]string{"APP_ENV": "production"}, Command: []string{"serve"}})
	if err != nil {
		t.Fatal(err)
	}
	if status.Desired != 2 || status.Available != 2 {
		t.Fatalf("status = %#v", status)
	}
	want := []string{"--kubeconfig", "/etc/rancher/k3s.yaml", "--namespace", "apps", "apply", "-f", "-"}
	if !reflect.DeepEqual(runner.calls[0].args, want) {
		t.Fatalf("apply args = %#v", runner.calls[0].args)
	}
	var manifest map[string]any
	if err := json.Unmarshal(runner.calls[0].input, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["kind"] != "List" {
		t.Fatalf("manifest = %#v", manifest)
	}
	items := manifest["items"].([]any)
	deployment := items[0].(map[string]any)
	service := items[1].(map[string]any)
	if deployment["kind"] != "Deployment" || service["kind"] != "Service" {
		t.Fatalf("items = %#v", items)
	}
	spec := deployment["spec"].(map[string]any)
	template := spec["template"].(map[string]any)
	podSpec := template["spec"].(map[string]any)
	container := podSpec["containers"].([]any)[0].(map[string]any)
	if len(container["env"].([]any)) != 1 || len(container["args"].([]any)) != 1 {
		t.Fatalf("container = %#v", container)
	}
}

func TestK3sStatusReturnsRoutableReadyPods(t *testing.T) {
	runner := &fakeKubectl{responses: [][]byte{
		[]byte(`{"metadata":{"labels":{"flatrun.port":"8080"}},"spec":{"replicas":1},"status":{"availableReplicas":1}}`),
		[]byte(`{"items":[{"metadata":{"name":"shop-a"},"spec":{"nodeName":"prod2"},"status":{"podIP":"10.42.0.8","phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}]}`),
	}}
	provider := NewK3sProvider("", "apps")
	provider.runner = runner

	status, err := provider.Status(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Instances) != 1 || status.Instances[0].Address != "10.42.0.8:8080" || !status.Instances[0].Ready {
		t.Fatalf("status = %#v", status)
	}
}

func TestK3sMetricsReturnsLimitUtilization(t *testing.T) {
	runner := &fakeKubectl{responses: [][]byte{
		[]byte(`{"spec":{"replicas":2,"template":{"spec":{"containers":[{"resources":{"limits":{"cpu":"500m","memory":"256Mi"}}}]}}}}`),
		[]byte(`{"items":[{"containers":[{"usage":{"cpu":"250m","memory":"128Mi"}}]},{"containers":[{"usage":{"cpu":"500m","memory":"256Mi"}}]}]}`),
	}}
	provider := NewK3sProvider("", "apps")
	provider.runner = runner
	usage, err := provider.Metrics(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if usage.CPUPercent != 75 || usage.MemoryPercent != 75 {
		t.Fatalf("usage = %#v", usage)
	}
}
