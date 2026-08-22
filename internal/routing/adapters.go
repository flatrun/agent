package routing

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type ConfigWriter interface {
	Apply(context.Context, string, string, []byte) error
	Remove(context.Context, string) error
}

type routeProvider struct {
	id     ProviderID
	writer ConfigWriter
	render func(Route) ([]byte, error)
	mu     sync.RWMutex
	routes map[string]Route
}

func NewNginxProvider(writer ConfigWriter) Provider {
	return &routeProvider{id: ProviderNginx, writer: writer, render: renderNginx, routes: make(map[string]Route)}
}

func NewTraefikProvider(writer ConfigWriter) Provider {
	return &routeProvider{id: ProviderTraefik, writer: writer, render: renderTraefik, routes: make(map[string]Route)}
}

func (p *routeProvider) ID() ProviderID {
	return p.id
}

func (p *routeProvider) Validate(_ context.Context, route Route) error {
	if !safeRouteID.MatchString(route.ID) {
		return fmt.Errorf("Route ID is invalid")
	}
	if strings.TrimSpace(route.Domain) == "" || strings.ContainsAny(route.Domain, " /\\") {
		return fmt.Errorf("Route domain is invalid")
	}
	if route.Protocol != "http" && route.Protocol != "https" {
		return fmt.Errorf("Route protocol must be http or https")
	}
	if route.Path != "" && !strings.HasPrefix(route.Path, "/") {
		return fmt.Errorf("Route path must start with a slash")
	}
	if len(route.Backends) == 0 {
		return fmt.Errorf("Route needs at least one backend")
	}
	for _, backend := range route.Backends {
		if strings.TrimSpace(backend.ID) == "" {
			return fmt.Errorf("Backend ID is required")
		}
		host, portValue, err := net.SplitHostPort(backend.Address)
		if err != nil {
			return fmt.Errorf("Backend %q address is invalid: %w", backend.ID, err)
		}
		if net.ParseIP(host) == nil && !safeHost.MatchString(host) {
			return fmt.Errorf("Backend %q host is invalid", backend.ID)
		}
		port, err := strconv.Atoi(portValue)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("Backend %q port is invalid", backend.ID)
		}
		if backend.Weight < 0 {
			return fmt.Errorf("Backend %q weight cannot be negative", backend.ID)
		}
	}
	return nil
}

func (p *routeProvider) Reconcile(ctx context.Context, route Route) error {
	if err := p.Validate(ctx, route); err != nil {
		return err
	}
	content, err := p.render(route)
	if err != nil {
		return err
	}
	if err := p.writer.Apply(ctx, route.ID, string(p.id), content); err != nil {
		return err
	}
	p.mu.Lock()
	p.routes[route.ID] = route
	p.mu.Unlock()
	return nil
}

func (p *routeProvider) Drain(ctx context.Context, routeID, backendID string) error {
	p.mu.RLock()
	route, ok := p.routes[routeID]
	p.mu.RUnlock()
	if !ok {
		return fmt.Errorf("Route %q is not managed", routeID)
	}
	found := false
	for index := range route.Backends {
		if route.Backends[index].ID == backendID {
			route.Backends[index].Healthy = false
			found = true
		}
	}
	if !found {
		return fmt.Errorf("Backend %q is not part of route %q", backendID, routeID)
	}
	return p.Reconcile(ctx, route)
}

func (p *routeProvider) Remove(ctx context.Context, routeID string) error {
	if err := p.writer.Remove(ctx, routeID); err != nil {
		return err
	}
	p.mu.Lock()
	delete(p.routes, routeID)
	p.mu.Unlock()
	return nil
}

var safeID = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)
var safeHost = regexp.MustCompile(`^[a-zA-Z0-9.-]+$`)
var safeRouteID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func renderNginx(route Route) ([]byte, error) {
	name := "flatrun_" + safeID.ReplaceAllString(route.ID, "_")
	path := route.Path
	if path == "" {
		path = "/"
	}
	backends := append([]Backend(nil), route.Backends...)
	sort.Slice(backends, func(i, j int) bool { return backends[i].ID < backends[j].ID })
	var output strings.Builder
	fmt.Fprintf(&output, "upstream %s {\n", name)
	for _, backend := range backends {
		fmt.Fprintf(&output, "    server %s", backend.Address)
		if backend.Weight > 0 {
			fmt.Fprintf(&output, " weight=%d", backend.Weight)
		}
		if !backend.Healthy {
			output.WriteString(" down")
		}
		output.WriteString(";\n")
	}
	output.WriteString("}\n\nserver {\n    listen 80;\n")
	fmt.Fprintf(&output, "    server_name %s;\n\n", route.Domain)
	fmt.Fprintf(&output, "    location %s {\n", path)
	fmt.Fprintf(&output, "        proxy_pass %s://%s;\n", route.Protocol, name)
	output.WriteString("        proxy_set_header Host $host;\n        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n    }\n}\n")
	return []byte(output.String()), nil
}

func renderTraefik(route Route) ([]byte, error) {
	path := route.Path
	if path == "" {
		path = "/"
	}
	servers := make([]map[string]string, 0, len(route.Backends))
	for _, backend := range route.Backends {
		if backend.Healthy {
			servers = append(servers, map[string]string{"url": route.Protocol + "://" + backend.Address})
		}
	}
	config := map[string]any{"http": map[string]any{
		"routers": map[string]any{route.ID: map[string]any{
			"rule":    fmt.Sprintf("Host(`%s`) && PathPrefix(`%s`)", route.Domain, path),
			"service": route.ID,
		}},
		"services": map[string]any{route.ID: map[string]any{
			"loadBalancer": map[string]any{"servers": servers},
		}},
	}}
	return yaml.Marshal(config)
}
