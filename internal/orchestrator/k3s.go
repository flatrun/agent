package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

type kubectlRunner interface {
	Run(context.Context, []byte, ...string) ([]byte, error)
}

type commandKubectl struct {
	binary string
}

func (r commandKubectl) Run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.binary, args...)
	cmd.Stdin = bytes.NewReader(input)
	return cmd.CombinedOutput()
}

type K3sProvider struct {
	runner     kubectlRunner
	kubeconfig string
	namespace  string
}

func NewK3sProvider(kubeconfig, namespace string) *K3sProvider {
	if namespace == "" {
		namespace = "default"
	}
	return &K3sProvider{
		runner:     commandKubectl{binary: "kubectl"},
		kubeconfig: strings.TrimSpace(kubeconfig),
		namespace:  strings.TrimSpace(namespace),
	}
}

func (p *K3sProvider) ID() ProviderID { return ProviderK3s }

func (p *K3sProvider) Ready(ctx context.Context) error {
	if _, err := p.run(ctx, nil, "cluster-info"); err != nil {
		return fmt.Errorf("K3s is not available: %w", err)
	}
	return nil
}

func (p *K3sProvider) Validate(_ context.Context, workload Workload) error {
	if strings.TrimSpace(workload.ID) == "" {
		return fmt.Errorf("Workload ID is required")
	}
	if strings.TrimSpace(workload.Image) == "" {
		return fmt.Errorf("Workload image is required")
	}
	if workload.Replicas < 1 {
		return fmt.Errorf("Replicas must be at least one")
	}
	if workload.Stateful && workload.Replicas > 1 {
		return fmt.Errorf("Stateful workloads cannot use multiple replicas without a storage policy")
	}
	if workload.Port < 0 || workload.Port > 65535 {
		return fmt.Errorf("Workload port is invalid")
	}
	return nil
}

func (p *K3sProvider) Apply(ctx context.Context, workload Workload) (Status, error) {
	if err := p.Validate(ctx, workload); err != nil {
		return Status{}, err
	}
	manifest, err := json.Marshal(k3sManifest(workload))
	if err != nil {
		return Status{}, err
	}
	if _, err := p.run(ctx, manifest, "apply", "-f", "-"); err != nil {
		return Status{}, fmt.Errorf("apply K3s workload: %w", err)
	}
	return p.Status(ctx, workload.ID)
}

func (p *K3sProvider) Resize(ctx context.Context, id string, resources Resources) (Status, error) {
	patch := map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{
		"name": id, "resources": k3sResources(resources),
	}}}}}}
	encoded, _ := json.Marshal(patch)
	if _, err := p.run(ctx, nil, "patch", "deployment", id, "--type", "merge", "-p", string(encoded)); err != nil {
		return Status{}, fmt.Errorf("resize K3s workload: %w", err)
	}
	return p.Status(ctx, id)
}

func (p *K3sProvider) Scale(ctx context.Context, id string, replicas int) (Status, error) {
	if replicas < 0 {
		return Status{}, fmt.Errorf("Replicas cannot be negative")
	}
	if _, err := p.run(ctx, nil, "scale", "deployment", id, "--replicas", strconv.Itoa(replicas)); err != nil {
		return Status{}, fmt.Errorf("scale K3s workload: %w", err)
	}
	return p.Status(ctx, id)
}

func (p *K3sProvider) Status(ctx context.Context, id string) (Status, error) {
	deploymentRaw, err := p.run(ctx, nil, "get", "deployment", id, "-o", "json")
	if err != nil {
		return Status{}, fmt.Errorf("inspect K3s workload: %w", err)
	}
	var deployment struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			Replicas int `json:"replicas"`
		} `json:"spec"`
		Status struct {
			Available int `json:"availableReplicas"`
		} `json:"status"`
	}
	if err := json.Unmarshal(deploymentRaw, &deployment); err != nil {
		return Status{}, fmt.Errorf("decode K3s workload: %w", err)
	}
	podsRaw, err := p.run(ctx, nil, "get", "pods", "-l", "flatrun.workload="+id, "-o", "json")
	if err != nil {
		return Status{}, fmt.Errorf("list K3s workload pods: %w", err)
	}
	var pods struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Node string `json:"nodeName"`
			} `json:"spec"`
			Status struct {
				IP         string                          `json:"podIP"`
				Phase      string                          `json:"phase"`
				Conditions []struct{ Type, Status string } `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(podsRaw, &pods); err != nil {
		return Status{}, fmt.Errorf("decode K3s workload pods: %w", err)
	}
	status := Status{Workload: id, Desired: deployment.Spec.Replicas, Available: deployment.Status.Available}
	port := deployment.Metadata.Labels["flatrun.port"]
	for _, pod := range pods.Items {
		ready := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				ready = true
			}
		}
		address := ""
		if pod.Status.IP != "" && port != "" {
			address = net.JoinHostPort(pod.Status.IP, port)
		}
		status.Instances = append(status.Instances, Instance{ID: pod.Metadata.Name, Node: pod.Spec.Node, Address: address, Healthy: pod.Status.Phase == "Running", Ready: ready})
	}
	return status, nil
}

func (p *K3sProvider) Metrics(ctx context.Context, id string) (Usage, error) {
	deploymentRaw, err := p.run(ctx, nil, "get", "deployment", id, "-o", "json")
	if err != nil {
		return Usage{}, fmt.Errorf("inspect K3s workload resources: %w", err)
	}
	var deployment struct {
		Spec struct {
			Replicas int `json:"replicas"`
			Template struct {
				Spec struct {
					Containers []struct {
						Resources struct {
							Limits map[string]string `json:"limits"`
						} `json:"resources"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(deploymentRaw, &deployment); err != nil {
		return Usage{}, fmt.Errorf("decode K3s workload resources: %w", err)
	}
	if deployment.Spec.Replicas < 1 || len(deployment.Spec.Template.Spec.Containers) == 0 {
		return Usage{}, fmt.Errorf("K3s workload has no measurable replicas")
	}
	limits := deployment.Spec.Template.Spec.Containers[0].Resources.Limits
	cpuLimit, err := parseCPUQuantity(limits["cpu"])
	if err != nil || cpuLimit <= 0 {
		return Usage{}, fmt.Errorf("K3s workload needs a CPU limit for autoscaling")
	}
	memoryLimit, err := parseMemoryQuantity(limits["memory"])
	if err != nil || memoryLimit <= 0 {
		return Usage{}, fmt.Errorf("K3s workload needs a memory limit for autoscaling")
	}
	metricsRaw, err := p.run(ctx, nil, "get", "--raw", "/apis/metrics.k8s.io/v1beta1/namespaces/"+p.namespace+"/pods?labelSelector=flatrun.workload%3D"+id)
	if err != nil {
		return Usage{}, fmt.Errorf("read K3s Metrics API: %w", err)
	}
	var metrics struct {
		Items []struct {
			Containers []struct {
				Usage map[string]string `json:"usage"`
			} `json:"containers"`
		} `json:"items"`
	}
	if err := json.Unmarshal(metricsRaw, &metrics); err != nil {
		return Usage{}, fmt.Errorf("decode K3s Metrics API: %w", err)
	}
	var cpuUsed float64
	var memoryUsed float64
	for _, pod := range metrics.Items {
		for _, container := range pod.Containers {
			cpu, cpuErr := parseCPUQuantity(container.Usage["cpu"])
			memory, memoryErr := parseMemoryQuantity(container.Usage["memory"])
			if cpuErr != nil || memoryErr != nil {
				return Usage{}, fmt.Errorf("decode K3s container usage")
			}
			cpuUsed += cpu
			memoryUsed += memory
		}
	}
	replicas := float64(deployment.Spec.Replicas)
	return Usage{
		CPUPercent:    cpuUsed / (cpuLimit * replicas) * 100,
		MemoryPercent: memoryUsed / (memoryLimit * replicas) * 100,
	}, nil
}

func parseCPUQuantity(value string) (float64, error) {
	multiplier := 1.0
	for suffix, factor := range map[string]float64{"n": 1e-9, "u": 1e-6, "m": 1e-3} {
		if strings.HasSuffix(value, suffix) {
			multiplier = factor
			value = strings.TrimSuffix(value, suffix)
			break
		}
	}
	parsed, err := strconv.ParseFloat(value, 64)
	return parsed * multiplier, err
}

func parseMemoryQuantity(value string) (float64, error) {
	multipliers := map[string]float64{
		"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40,
		"K": 1e3, "M": 1e6, "G": 1e9, "T": 1e12,
	}
	multiplier := 1.0
	for suffix, factor := range multipliers {
		if strings.HasSuffix(value, suffix) {
			multiplier = factor
			value = strings.TrimSuffix(value, suffix)
			break
		}
	}
	parsed, err := strconv.ParseFloat(value, 64)
	return parsed * multiplier, err
}

func (p *K3sProvider) Remove(ctx context.Context, id string) error {
	if _, err := p.run(ctx, nil, "delete", "deployment", id, "--ignore-not-found=true"); err != nil {
		return fmt.Errorf("remove K3s workload: %w", err)
	}
	return nil
}

func (p *K3sProvider) run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	base := make([]string, 0, len(args)+4)
	if p.kubeconfig != "" {
		base = append(base, "--kubeconfig", p.kubeconfig)
	}
	base = append(base, "--namespace", p.namespace)
	output, err := p.runner.Run(ctx, input, append(base, args...)...)
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message != "" {
			return nil, fmt.Errorf("%s", message)
		}
		return nil, err
	}
	return output, nil
}

func k3sManifest(workload Workload) map[string]any {
	labels := map[string]string{"app.kubernetes.io/name": workload.ID, "flatrun.workload": workload.ID}
	for key, value := range workload.Labels {
		labels[key] = value
	}
	if workload.Port > 0 {
		labels["flatrun.port"] = strconv.Itoa(workload.Port)
	}
	container := map[string]any{"name": workload.ID, "image": workload.Image, "resources": k3sResources(workload.Resources)}
	if len(workload.Environment) > 0 {
		keys := make([]string, 0, len(workload.Environment))
		for key := range workload.Environment {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		environment := make([]any, 0, len(keys))
		for _, key := range keys {
			environment = append(environment, map[string]string{"name": key, "value": workload.Environment[key]})
		}
		container["env"] = environment
	}
	if len(workload.Entrypoint) > 0 {
		container["command"] = workload.Entrypoint
	}
	if len(workload.Command) > 0 {
		container["args"] = workload.Command
	}
	if workload.WorkingDir != "" {
		container["workingDir"] = workload.WorkingDir
	}
	if workload.Port > 0 {
		container["ports"] = []any{map[string]any{"containerPort": workload.Port}}
	}
	deployment := map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": workload.ID, "labels": labels},
		"spec": map[string]any{"replicas": workload.Replicas, "selector": map[string]any{"matchLabels": map[string]string{"flatrun.workload": workload.ID}}, "template": map[string]any{
			"metadata": map[string]any{"labels": labels}, "spec": map[string]any{"containers": []any{container}},
		}},
	}
	items := []any{deployment}
	if workload.Port > 0 {
		items = append(items, map[string]any{
			"apiVersion": "v1", "kind": "Service",
			"metadata": map[string]any{"name": workload.ID, "labels": labels},
			"spec": map[string]any{
				"selector": map[string]string{"flatrun.workload": workload.ID},
				"ports":    []any{map[string]any{"name": "http", "port": workload.Port, "targetPort": workload.Port}},
			},
		})
	}
	return map[string]any{"apiVersion": "v1", "kind": "List", "items": items}
}

func k3sResources(resources Resources) map[string]any {
	requests := map[string]string{}
	limits := map[string]string{}
	if resources.CPURequest > 0 {
		requests["cpu"] = fmt.Sprintf("%gm", resources.CPURequest*1000)
	}
	if resources.MemoryRequest > 0 {
		requests["memory"] = strconv.FormatUint(resources.MemoryRequest, 10)
	}
	if resources.CPULimit > 0 {
		limits["cpu"] = fmt.Sprintf("%gm", resources.CPULimit*1000)
	}
	if resources.MemoryLimit > 0 {
		limits["memory"] = strconv.FormatUint(resources.MemoryLimit, 10)
	}
	return map[string]any{"requests": requests, "limits": limits}
}
