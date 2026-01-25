package dns

import (
	"github.com/flatrun/agent/pkg/plugins"
	"github.com/gin-gonic/gin"
)

type BaseDNSPlugin struct {
	info plugins.PluginInfo
}

func (p *BaseDNSPlugin) Info() plugins.PluginInfo {
	return p.info
}

func (p *BaseDNSPlugin) Initialize(config map[string]interface{}) error {
	return nil
}

func (p *BaseDNSPlugin) Start() error {
	return nil
}

func (p *BaseDNSPlugin) Stop() error {
	return nil
}

func (p *BaseDNSPlugin) GetCapabilities() []plugins.Capability {
	return []plugins.Capability{
		plugins.CapDNSZoneManagement,
		plugins.CapDNSRecordManagement,
	}
}

func (p *BaseDNSPlugin) RegisterRoutes(router *gin.RouterGroup) error {
	return nil
}

func (p *BaseDNSPlugin) GetWidgetData(deploymentName string) (interface{}, error) {
	return nil, nil
}
