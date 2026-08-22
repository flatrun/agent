package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

type KubernetesRunner interface {
	Run(context.Context, []byte, ...string) ([]byte, error)
}

type kubectlCommand struct{}

func (kubectlCommand) Run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "kubectl", args...)
	command.Stdin = bytes.NewReader(input)
	return command.CombinedOutput()
}

type K3sIngressProvider struct {
	runner     KubernetesRunner
	kubeconfig string
	namespace  string
	mu         sync.RWMutex
	routes     map[string]Route
}

func NewK3sIngressProvider(kubeconfig, namespace string) Provider {
	if strings.TrimSpace(namespace) == "" {
		namespace = "default"
	}
	return &K3sIngressProvider{
		runner: kubectlCommand{}, kubeconfig: strings.TrimSpace(kubeconfig),
		namespace: strings.TrimSpace(namespace), routes: make(map[string]Route),
	}
}

func NewK3sIngressProviderWithRunner(kubeconfig, namespace string, runner KubernetesRunner) Provider {
	provider := NewK3sIngressProvider(kubeconfig, namespace).(*K3sIngressProvider)
	provider.runner = runner
	return provider
}

func (p *K3sIngressProvider) ID() ProviderID { return ProviderTraefik }

func (p *K3sIngressProvider) Validate(_ context.Context, route Route) error {
	if err := validateRoute(route); err != nil {
		return err
	}
	if strings.TrimSpace(route.Service) == "" {
		return fmt.Errorf("Route service is required")
	}
	return nil
}

func (p *K3sIngressProvider) Reconcile(ctx context.Context, route Route) error {
	if err := p.Validate(ctx, route); err != nil {
		return err
	}
	_, portValue, err := net.SplitHostPort(route.Backends[0].Address)
	if err != nil {
		return fmt.Errorf("resolve route service port: %w", err)
	}
	port, err := strconv.Atoi(portValue)
	if err != nil {
		return fmt.Errorf("resolve route service port: %w", err)
	}
	path := route.Path
	if path == "" {
		path = "/"
	}
	manifest := map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "Ingress",
		"metadata": map[string]any{"name": route.ID, "labels": map[string]string{"flatrun.route": route.ID}},
		"spec": map[string]any{"rules": []any{map[string]any{
			"host": route.Domain, "http": map[string]any{"paths": []any{map[string]any{
				"path": path, "pathType": "Prefix", "backend": map[string]any{"service": map[string]any{
					"name": route.Service, "port": map[string]any{"number": port},
				}},
			}}},
		}}},
	}
	content, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if _, err := p.run(ctx, content, "apply", "-f", "-"); err != nil {
		return fmt.Errorf("apply K3s ingress: %w", err)
	}
	p.mu.Lock()
	p.routes[route.ID] = route
	p.mu.Unlock()
	return nil
}

func (p *K3sIngressProvider) Drain(_ context.Context, routeID, backendID string) error {
	p.mu.RLock()
	_, exists := p.routes[routeID]
	p.mu.RUnlock()
	if !exists {
		return fmt.Errorf("Route %q is not managed", routeID)
	}
	return nil
}

func (p *K3sIngressProvider) Remove(ctx context.Context, routeID string) error {
	if !safeRouteID.MatchString(routeID) {
		return fmt.Errorf("Route ID is invalid")
	}
	if _, err := p.run(ctx, nil, "delete", "ingress", routeID, "--ignore-not-found=true"); err != nil {
		return fmt.Errorf("remove K3s ingress: %w", err)
	}
	p.mu.Lock()
	delete(p.routes, routeID)
	p.mu.Unlock()
	return nil
}

func (p *K3sIngressProvider) run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	base := make([]string, 0, len(args)+4)
	if p.kubeconfig != "" {
		base = append(base, "--kubeconfig", p.kubeconfig)
	}
	base = append(base, "--namespace", p.namespace)
	output, err := p.runner.Run(ctx, input, append(base, args...)...)
	if err != nil {
		if message := strings.TrimSpace(string(output)); message != "" {
			return nil, fmt.Errorf("%s", message)
		}
		return nil, err
	}
	return output, nil
}
