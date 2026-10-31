package firewall

import (
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/flatrun/agent/pkg/plugins"
	"github.com/gin-gonic/gin"
)

// Plugin is the built-in Firewall app. It implements plugins.Plugin so it registers in the
// plugin registry and lists as an installed app.
type Plugin struct {
	mu       sync.RWMutex
	enforced bool
	store    *Store
	runner   nftRunner // nil in production; a fake is injected in tests
}

func New(store *Store) *Plugin { return &Plugin{store: store} }

// EnforceCurrent applies the stored config to the host firewall. The server calls
// it at startup so a saved policy takes effect again after a restart.
func (p *Plugin) EnforceCurrent() (bool, error) {
	if _, err := os.Stat(p.store.path); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	cfg, err := p.store.Load()
	if err != nil {
		return false, err
	}
	return p.apply(cfg)
}

func (p *Plugin) apply(cfg *Config) (bool, error) {
	enforced, err := Apply(cfg, p.runner)
	p.mu.Lock()
	p.enforced = enforced && err == nil
	p.mu.Unlock()
	return enforced, err
}
func (p *Plugin) status(cfg *Config) string {
	if !cfg.Enabled {
		return "disabled"
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.enforced {
		table, err := p.runner.ListTable()
		if err == nil && strings.TrimSpace(table) != "" {
			return "applied"
		}
	}
	return "unhealthy"
}

func (p *Plugin) Info() plugins.PluginInfo {
	return plugins.PluginInfo{
		Name:         "firewall",
		Version:      "0.1.0",
		DisplayName:  "Firewall",
		Description:  "Set and enforce the server's inbound and outbound traffic rules in one place.",
		Author:       "FlatRun",
		Type:         plugins.TypeIntegration,
		Category:     "security",
		Enabled:      true,
		Capabilities: []string{"host_firewall"},
	}
}

func (p *Plugin) GetCapabilities() []plugins.Capability {
	return []plugins.Capability{"host_firewall"}
}

func (p *Plugin) GetWidgetData(deploymentName string) (interface{}, error) {
	cfg, err := p.store.Load()
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"plugin":      "firewall",
		"enabled":     cfg.Enabled,
		"rules":       len(cfg.Rules),
		"enforcement": p.status(cfg),
	}, nil
}

// RegisterRoutes exposes the host firewall config and a read-only plan. Saving validates the
// config but does not yet enforce it.
func (p *Plugin) RegisterRoutes(router *gin.RouterGroup) error {
	router.GET("/firewall", func(c *gin.Context) {
		cfg, err := p.store.Load()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		cfg.Enforcement = p.status(cfg)
		c.JSON(http.StatusOK, cfg)
	})

	router.PUT("/firewall", func(c *gin.Context) {
		var cfg Config
		if err := c.ShouldBindJSON(&cfg); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
			return
		}
		if err := p.store.Save(&cfg); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		enforced, err := p.apply(&cfg)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "saved": true, "enforced": false})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "firewall config saved", "enforced": enforced, "enforcement": p.status(&cfg)})
	})

	router.GET("/firewall/plan", func(c *gin.Context) {
		cfg, err := p.store.Load()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"plan": Plan(cfg), "available": enforcementAvailable(p.runner)})
	})
	return nil
}

// enforcementAvailable reports whether the host can enforce firewall rules (nft
// present), so the UI can tell an operator when saving will only persist.
func enforcementAvailable(runner nftRunner) bool {
	if runner == nil {
		runner = newExecNftRunner()
	}
	return runner.Available()
}
