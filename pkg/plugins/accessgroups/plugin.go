// Package accessgroups is a built-in FlatRun app (plugin) that models per-deployment
// east-west and egress access policy, AWS security-group style: allow flows to peer
// deployments or external CIDRs, with a default egress stance. Rules are stored in a
// deployment's service.yml under `access_groups`.
//
// This is a scaffold: Apply is a no-op. When enforcement lands it will operate at the
// Docker-network / L3-L4 layer (dedicated/internal networks, selective connect, or
// iptables) and must not write nginx config or IP blocks: HTTP ingress and rate limiting
// are owned by the security module.
package accessgroups

import (
	"fmt"
	"net/http"

	"github.com/flatrun/agent/pkg/models"
	"github.com/flatrun/agent/pkg/plugins"
	"github.com/gin-gonic/gin"
)

const (
	EgressAllowAll = "allow-all"
	EgressDenyAll  = "deny-all"
)

// Plugin is the built-in Access Groups app. It implements plugins.Plugin so it can be
// registered in the plugin registry and listed as an installed app.
type Plugin struct {
	// policyFor loads a deployment's stored access-groups config so the plan endpoint can
	// describe real rules. May be nil (then plan returns an empty policy).
	policyFor func(deployment string) (*models.AccessGroupsConfig, error)
}

func New(policyFor func(deployment string) (*models.AccessGroupsConfig, error)) *Plugin {
	return &Plugin{policyFor: policyFor}
}

func (p *Plugin) Info() plugins.PluginInfo {
	return plugins.PluginInfo{
		Name:         "access-groups",
		Version:      "0.1.0",
		DisplayName:  "Access Groups",
		Description:  "Control which deployments may reach each other and what they can reach (east-west and egress rules). Enforcement coming soon.",
		Author:       "FlatRun",
		Type:         plugins.TypeIntegration,
		Category:     "networking",
		Enabled:      true,
		Capabilities: []string{"network_policy"},
	}
}

func (p *Plugin) Initialize(config map[string]interface{}) error { return nil }
func (p *Plugin) Start() error                                   { return nil }
func (p *Plugin) Stop() error                                    { return nil }

func (p *Plugin) GetCapabilities() []plugins.Capability {
	return []plugins.Capability{"network_policy"}
}

func (p *Plugin) GetWidgetData(deploymentName string) (interface{}, error) {
	policy, _ := p.load(deploymentName)
	count := 0
	enabled := false
	if policy != nil {
		count = len(policy.Allow)
		enabled = policy.Enabled
	}
	return map[string]interface{}{
		"plugin":  "access-groups",
		"name":    deploymentName,
		"enabled": enabled,
		"rules":   count,
	}, nil
}

// RegisterRoutes exposes a read-only plan endpoint so the UI can preview the flows a
// deployment's access-groups config would permit (enforcement is not wired yet).
func (p *Plugin) RegisterRoutes(router *gin.RouterGroup) error {
	router.GET("/deployments/:name/access-groups/plan", func(c *gin.Context) {
		policy, err := p.load(c.Param("name"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "deployment not found"})
			return
		}
		if err := Validate(policy); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"plan": Plan(policy), "enforced": false})
	})
	return nil
}

func (p *Plugin) load(deployment string) (*models.AccessGroupsConfig, error) {
	if p.policyFor == nil {
		return nil, nil
	}
	return p.policyFor(deployment)
}

// Validate checks that a policy is well formed before it is saved or applied.
func Validate(policy *models.AccessGroupsConfig) error {
	if policy == nil {
		return nil
	}
	switch policy.Egress {
	case "", EgressAllowAll, EgressDenyAll:
	default:
		return fmt.Errorf("access_groups.egress must be %q or %q, got %q", EgressAllowAll, EgressDenyAll, policy.Egress)
	}
	for i, r := range policy.Allow {
		if (r.To == "") == (r.CIDR == "") {
			return fmt.Errorf("access_groups.allow[%d] must set exactly one of `to` (peer deployment) or `cidr` (external)", i)
		}
		if r.Port < 0 || r.Port > 65535 {
			return fmt.Errorf("access_groups.allow[%d] port %d out of range", i, r.Port)
		}
		if r.Protocol != "" && r.Protocol != "tcp" && r.Protocol != "udp" {
			return fmt.Errorf("access_groups.allow[%d] protocol must be tcp or udp, got %q", i, r.Protocol)
		}
	}
	return nil
}

// Plan returns a human-readable description of the flows the policy would permit, so the
// UI and tests can exercise the rule shape before real enforcement exists.
func Plan(policy *models.AccessGroupsConfig) []string {
	if policy == nil || !policy.Enabled {
		return nil
	}

	egress := policy.Egress
	if egress == "" {
		egress = EgressAllowAll
	}
	plan := []string{fmt.Sprintf("default egress: %s", egress)}

	for _, r := range policy.Allow {
		proto := r.Protocol
		if proto == "" {
			proto = "tcp"
		}
		target, kind := r.To, "deployment"
		if target == "" {
			target, kind = r.CIDR, "cidr"
		}
		port := "any"
		if r.Port != 0 {
			port = fmt.Sprintf("%d", r.Port)
		}
		plan = append(plan, fmt.Sprintf("allow %s/%s -> %s %s", proto, port, kind, target))
	}
	return plan
}

// Apply would reconcile host/Docker network state to match the policy. It is a no-op
// scaffold that only validates the policy; no network state is changed yet.
func Apply(deployment string, policy *models.AccessGroupsConfig) error {
	// TODO: enforce east-west + egress rules via Docker internal networks / iptables.
	return Validate(policy)
}
