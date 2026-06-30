package plugins

import (
	"context"

	"github.com/gin-gonic/gin"
)

type PluginType string

const (
	TypeDeployment  PluginType = "deployment"
	TypeWidget      PluginType = "widget"
	TypeService     PluginType = "service"
	TypeIntegration PluginType = "integration"
	TypeDNS         PluginType = "dns"
)

type Capability string

const (
	CapAutoSSL             Capability = "auto_ssl"
	CapAutoBackup          Capability = "auto_backup"
	CapAutoUpdate          Capability = "auto_update"
	CapMonitoring          Capability = "monitoring"
	CapScaling             Capability = "scaling"
	CapDNSZoneManagement   Capability = "dns_zone_management"
	CapDNSRecordManagement Capability = "dns_record_management"
	CapDNSAutoConfig       Capability = "dns_auto_config"
)

type PluginInfo struct {
	Name                string                 `json:"name" yaml:"name"`
	Version             string                 `json:"version" yaml:"version"`
	DisplayName         string                 `json:"display_name" yaml:"display_name"`
	Description         string                 `json:"description" yaml:"description"`
	Author              string                 `json:"author" yaml:"author"`
	Type                PluginType             `json:"type" yaml:"type"`
	Category            string                 `json:"category" yaml:"category"`
	Enabled             bool                   `json:"enabled" yaml:"enabled"`
	Capabilities        []string               `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	Widget              *WidgetConfig          `json:"widget,omitempty" yaml:"widget,omitempty"`
	ConfigSchema        map[string]interface{} `json:"config_schema,omitempty" yaml:"config_schema,omitempty"`
	Requires            []string               `json:"requires,omitempty" yaml:"requires,omitempty"`
	Resources           *ResourceRequirements  `json:"resources,omitempty" yaml:"resources,omitempty"`
	DashboardExtensions []DashboardExtension   `json:"dashboard_extensions,omitempty" yaml:"dashboard_extensions,omitempty"`
	APIEndpoints        []APIEndpoint          `json:"api,omitempty" yaml:"api,omitempty"`
	Hooks               map[string]string      `json:"hooks,omitempty" yaml:"hooks,omitempty"`
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
	Enabled         bool           `json:"enabled" yaml:"enabled"`
	Position        string         `json:"position" yaml:"position"`
	Size            string         `json:"size" yaml:"size"`
	RefreshInterval int            `json:"refresh_interval" yaml:"refresh_interval"`
	Actions         []WidgetAction `json:"actions,omitempty" yaml:"actions,omitempty"`
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

// Plugin is the core interface every plugin implements: its identity and metadata.
// Everything beyond identity is opt-in through the capability interfaces below, so a plugin
// implements only what it needs. A firewall, for example, has no services to start or stop,
// so it does not implement LifecyclePlugin.
type Plugin interface {
	Info() PluginInfo
}

// ConfigurablePlugin accepts runtime configuration at startup.
type ConfigurablePlugin interface {
	Initialize(config map[string]interface{}) error
}

// LifecyclePlugin manages long-running state and is started and stopped with the agent.
type LifecyclePlugin interface {
	Start() error
	Stop() error
}

// RoutablePlugin serves its own HTTP endpoints; the host gives it the route group to mount on.
type RoutablePlugin interface {
	RegisterRoutes(router *gin.RouterGroup) error
}

// WidgetPlugin contributes dashboard widget data for a deployment.
type WidgetPlugin interface {
	GetWidgetData(deploymentName string) (interface{}, error)
}

// CapablePlugin declares the capabilities it provides.
type CapablePlugin interface {
	GetCapabilities() []Capability
}

// DeploymentPlugin creates and manages deployments from the plugin.
type DeploymentPlugin interface {
	Plugin
	CreateDeployment(name string, config map[string]interface{}) (*DeploymentResult, error)
	ConfigureDeployment(name string, config map[string]interface{}) error
	GetDeploymentStatus(name string) (*DeploymentStatus, error)
	GetDockerCompose(config map[string]interface{}) (string, error)
	GetNginxConfig(config map[string]interface{}) (string, error)
}

type DeploymentResult struct {
	Name        string            `json:"name"`
	Path        string            `json:"path"`
	Containers  []string          `json:"containers"`
	URLs        []string          `json:"urls"`
	Credentials map[string]string `json:"credentials,omitempty"`
}

type DeploymentStatus struct {
	Name    string                 `json:"name"`
	Status  string                 `json:"status"`
	Health  string                 `json:"health"`
	Metrics map[string]interface{} `json:"metrics"`
}

type DNSPlugin interface {
	Plugin
	ProviderName() string
	RequiredCredentials() []CredentialField
	SetCredentials(credentials map[string]string) error
	ValidateCredentials() error
	ListZones(ctx context.Context) ([]DNSZone, error)
	GetZone(ctx context.Context, zoneID string) (*DNSZone, error)
	ListRecords(ctx context.Context, zoneID string) ([]DNSRecord, error)
	CreateRecord(ctx context.Context, zoneID string, record DNSRecordCreate) (*DNSRecord, error)
	UpdateRecord(ctx context.Context, zoneID, recordID string, record DNSRecordUpdate) (*DNSRecord, error)
	DeleteRecord(ctx context.Context, zoneID, recordID string) error
}

type CredentialField struct {
	Name        string `json:"name" yaml:"name"`
	Label       string `json:"label" yaml:"label"`
	Type        string `json:"type" yaml:"type"`
	Required    bool   `json:"required" yaml:"required"`
	Placeholder string `json:"placeholder,omitempty" yaml:"placeholder,omitempty"`
	HelpText    string `json:"help_text,omitempty" yaml:"help_text,omitempty"`
}

type DNSZone struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	NameServers []string `json:"name_servers,omitempty"`
	RecordCount int      `json:"record_count"`
}

type DNSRecord struct {
	ID       string `json:"id"`
	ZoneID   string `json:"zone_id"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Priority *int   `json:"priority,omitempty"`
	Proxied  *bool  `json:"proxied,omitempty"`
}

type DNSRecordCreate struct {
	Type     string `json:"type" binding:"required"`
	Name     string `json:"name" binding:"required"`
	Content  string `json:"content" binding:"required"`
	TTL      int    `json:"ttl"`
	Priority *int   `json:"priority,omitempty"`
	Proxied  *bool  `json:"proxied,omitempty"`
}

type DNSRecordUpdate struct {
	Content  *string `json:"content,omitempty"`
	TTL      *int    `json:"ttl,omitempty"`
	Priority *int    `json:"priority,omitempty"`
	Proxied  *bool   `json:"proxied,omitempty"`
}
