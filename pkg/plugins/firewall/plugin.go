package firewall

import (
	"net/http"

	"github.com/flatrun/agent/pkg/plugins"
	"github.com/gin-gonic/gin"
)

// Plugin is the built-in Firewall app. It implements plugins.Plugin so it registers in the
// plugin registry and lists as an installed app.
type Plugin struct {
	store *Store
}

func New(store *Store) *Plugin { return &Plugin{store: store} }

func (p *Plugin) Info() plugins.PluginInfo {
	return plugins.PluginInfo{
		Name:         "firewall",
		Version:      "0.1.0",
		DisplayName:  "Firewall",
		Description:  "Set the server's inbound and outbound traffic rules in one place. Enforcement coming soon.",
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
		"plugin":  "firewall",
		"enabled": cfg.Enabled,
		"rules":   len(cfg.Rules),
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
		c.JSON(http.StatusOK, gin.H{"message": "firewall config saved", "enforced": false})
	})

	router.GET("/firewall/plan", func(c *gin.Context) {
		cfg, err := p.store.Load()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"plan": Plan(cfg), "enforced": false})
	})
	return nil
}
