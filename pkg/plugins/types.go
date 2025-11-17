package plugins

import "github.com/gin-gonic/gin"

type PluginType string

const (
	TypeDeployment  PluginType = "deployment"
	TypeWidget      PluginType = "widget"
	TypeService     PluginType = "service"
	TypeIntegration PluginType = "integration"
)

type Capability string

const (
	CapAutoSSL    Capability = "auto_ssl"
	CapAutoBackup Capability = "auto_backup"
	CapAutoUpdate Capability = "auto_update"
	CapMonitoring Capability = "monitoring"
	CapScaling    Capability = "scaling"
)

type PluginInfo struct {
	Name        string            `json:"name" yaml:"name"`
	Version     string            `json:"version" yaml:"version"`
	DisplayName string            `json:"display_name" yaml:"display_name"`
	Description string            `json:"description" yaml:"description"`
	Author      string            `json:"author" yaml:"author"`
	Type        PluginType        `json:"type" yaml:"type"`
	Category    string            `json:"category" yaml:"category"`
	Enabled     bool              `json:"enabled" yaml:"enabled"`
	Capabilities []string         `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	Widget      *WidgetConfig     `json:"widget,omitempty" yaml:"widget,omitempty"`
	ConfigSchema map[string]interface{} `json:"config_schema,omitempty" yaml:"config_schema,omitempty"`
	Requires    []string          `json:"requires,omitempty" yaml:"requires,omitempty"`
	Resources   *ResourceRequirements `json:"resources,omitempty" yaml:"resources,omitempty"`
	DashboardExtensions []DashboardExtension `json:"dashboard_extensions,omitempty" yaml:"dashboard_extensions,omitempty"`
	APIEndpoints []APIEndpoint   `json:"api,omitempty" yaml:"api,omitempty"`
	Hooks       map[string]string `json:"hooks,omitempty" yaml:"hooks,omitempty"`
}

type DashboardExtension struct {
	Location  string `json:"location" yaml:"location"`
	Component string `json:"component" yaml:"component"`
}

type APIEndpoint struct {
	Path    string `json:"path" yaml:"path"`
	Method  string `json:"method" yaml:"method"`
	Handler string `json:"handler" yaml:"handler"`
}

type WidgetConfig struct {
	Enabled         bool            `json:"enabled" yaml:"enabled"`
	Position        string          `json:"position" yaml:"position"`
	Size            string          `json:"size" yaml:"size"`
	RefreshInterval int             `json:"refresh_interval" yaml:"refresh_interval"`
	Actions         []WidgetAction  `json:"actions,omitempty" yaml:"actions,omitempty"`
}

type WidgetAction struct {
	Name  string `json:"name" yaml:"name"`
	Label string `json:"label" yaml:"label"`
	Icon  string `json:"icon" yaml:"icon"`
}

type ResourceRequirements struct {
	MinMemory         string `json:"min_memory" yaml:"min_memory"`
	MinCPU            string `json:"min_cpu" yaml:"min_cpu"`
	RecommendedMemory string `json:"recommended_memory" yaml:"recommended_memory"`
	RecommendedCPU    string `json:"recommended_cpu" yaml:"recommended_cpu"`
}

type Plugin interface {
	Info() PluginInfo
	Initialize(config map[string]interface{}) error
	Start() error
	Stop() error
	GetCapabilities() []Capability
	RegisterRoutes(router *gin.RouterGroup) error
	GetWidgetData(deploymentName string) (interface{}, error)
}

type DeploymentPlugin interface {
	Plugin
	CreateDeployment(name string, config map[string]interface{}) (*DeploymentResult, error)
	ConfigureDeployment(name string, config map[string]interface{}) error
	GetDeploymentStatus(name string) (*DeploymentStatus, error)
	GetDockerCompose(config map[string]interface{}) (string, error)
	GetNginxConfig(config map[string]interface{}) (string, error)
}

type DeploymentResult struct {
	Name       string            `json:"name"`
	Path       string            `json:"path"`
	Containers []string          `json:"containers"`
	URLs       []string          `json:"urls"`
	Credentials map[string]string `json:"credentials,omitempty"`
}

type DeploymentStatus struct {
	Name    string                 `json:"name"`
	Status  string                 `json:"status"`
	Health  string                 `json:"health"`
	Metrics map[string]interface{} `json:"metrics"`
}
