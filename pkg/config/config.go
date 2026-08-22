package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/flatrun/agent/pkg/models"
	"gopkg.in/yaml.v3"
)

type ClusterConfig struct {
	Enabled        bool      `yaml:"enabled"`
	ServerName     string    `yaml:"server_name"`
	AdvertiseURL   string    `yaml:"advertise_url"`
	HealthInterval string    `yaml:"health_interval"`
	RequestTimeout string    `yaml:"request_timeout"`
	Orchestrator   string    `yaml:"orchestrator" json:"orchestrator"`
	Routing        string    `yaml:"routing" json:"routing"`
	K3s            K3sConfig `yaml:"k3s" json:"k3s"`
}

type K3sConfig struct {
	Kubeconfig string `yaml:"kubeconfig" json:"kubeconfig"`
	Namespace  string `yaml:"namespace" json:"namespace"`
}

type CapacityConfig struct {
	AllocationThresholdPercent float64 `yaml:"allocation_threshold_percent" json:"allocation_threshold_percent"`
	HostThresholdPercent       float64 `yaml:"host_threshold_percent" json:"host_threshold_percent"`
	HostMemoryReserve          uint64  `yaml:"host_memory_reserve" json:"host_memory_reserve"`
	HostCPUReserve             float64 `yaml:"host_cpu_reserve" json:"host_cpu_reserve"`
	MemoryStepPercent          float64 `yaml:"memory_step_percent" json:"memory_step_percent"`
	CPUStepPercent             float64 `yaml:"cpu_step_percent" json:"cpu_step_percent"`
	MaxMemory                  uint64  `yaml:"max_memory" json:"max_memory"`
	MaxCPU                     float64 `yaml:"max_cpu" json:"max_cpu"`
	AllowVertical              *bool   `yaml:"allow_vertical" json:"allow_vertical"`
	AllowHorizontal            *bool   `yaml:"allow_horizontal" json:"allow_horizontal"`
	OfferToFleet               bool    `yaml:"offer_to_fleet" json:"offer_to_fleet"`
}

type Config struct {
	DeploymentsPath string               `yaml:"deployments_path"`
	SystemFilesRoot string               `yaml:"system_files_root"`
	DockerSocket    string               `yaml:"docker_socket"`
	DefaultTimeout  time.Duration        `yaml:"default_timeout"`
	API             APIConfig            `yaml:"api"`
	Auth            AuthConfig           `yaml:"auth"`
	Domain          DomainConfig         `yaml:"domain"`
	Nginx           NginxConfig          `yaml:"nginx"`
	Certbot         CertbotConfig        `yaml:"certbot"`
	Logging         LoggingConfig        `yaml:"logging"`
	Health          HealthConfig         `yaml:"health"`
	Infrastructure  InfrastructureConfig `yaml:"infrastructure"`
	Security        SecurityConfig       `yaml:"security"`
	Audit           AuditConfig          `yaml:"audit"`
	Cluster         ClusterConfig        `yaml:"cluster"`
	Capacity        CapacityConfig       `yaml:"capacity"`
	SystemTerminal  SystemTerminalConfig `yaml:"system_terminal"`
	Cleanup         CleanupConfig        `yaml:"cleanup"`
	Plans           PlansConfig          `yaml:"plans"`
	AI              AIConfig             `yaml:"ai"`
	MCP             MCPConfig            `yaml:"mcp"`
	Files           FilesConfig          `yaml:"files"`
	Backup          BackupConfig         `yaml:"backup"`
	Templates       TemplatesConfig      `yaml:"templates"`
}

// TemplatesConfig controls where the app template catalog is fetched from. The
// catalog lives outside the binary: it is synced from a source into an on-disk
// cache that the deploy and listing paths read. Infra and welcome content stay
// embedded and are unaffected.
type TemplatesConfig struct {
	// SyncInterval is the background resync period in seconds. A pointer so an
	// explicit 0, which disables the resync loop, is distinct from unset.
	SyncInterval *int                       `yaml:"sync_interval" json:"sync_interval"`
	GitHub       TemplatesGitHubConfig      `yaml:"github" json:"github"`
	Marketplace  TemplatesMarketplaceConfig `yaml:"marketplace" json:"marketplace"`
}

// TemplatesGitHubConfig is the GitHub-repo source, the working default today.
type TemplatesGitHubConfig struct {
	// Enabled defaults to true when unset; an explicit false is honored.
	Enabled *bool  `yaml:"enabled" json:"enabled"`
	Repo    string `yaml:"repo" json:"repo"`
	Ref     string `yaml:"ref" json:"ref"`
}

// TemplatesMarketplaceConfig is the marketplace-API source. Disabled by default
// until the API is declared ready; enabling it makes the marketplace
// authoritative ahead of GitHub with no code change.
type TemplatesMarketplaceConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// URL is the marketplace API root; empty falls back to the agent's default
	// marketplace endpoint.
	URL string `yaml:"url" json:"url"`
}

// MCPConfig controls the built-in MCP server that exposes the assistant's tool
// set to external MCP clients. Every call is authenticated and permission-gated
// exactly as the assistant is, so it is off unless deliberately enabled.
type MCPConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
}

type FilesConfig struct {
	// Pointer so an explicit false survives reloads; nil means "use default" (true).
	ShowHidden *bool `yaml:"show_hidden" json:"show_hidden"`
}

type BackupConfig struct {
	Destinations []BackupDestination `yaml:"destinations" json:"destinations"`
}

// BackupDestination is an object store that backups are mirrored to after the
// local copy is written. Secrets are never stored here: CredentialID references
// an S3 credential held by the credential manager.
//
// Kind records who runs the store: "external" (a store FlatRun only connects to)
// or "managed" (a store FlatRun runs itself, deployed from a template). For a
// managed store, Deployment names the deployment that runs the container. See
// docs/OBJECT_STORES.md.
type BackupDestination struct {
	Name         string `yaml:"name" json:"name"`
	Type         string `yaml:"type" json:"type"`
	Kind         string `yaml:"kind" json:"kind"`
	Deployment   string `yaml:"deployment,omitempty" json:"deployment,omitempty"`
	Endpoint     string `yaml:"endpoint" json:"endpoint"`
	Region       string `yaml:"region" json:"region"`
	Bucket       string `yaml:"bucket" json:"bucket"`
	Prefix       string `yaml:"prefix" json:"prefix"`
	CredentialID string `yaml:"credential_id" json:"credential_id"`
	UsePathStyle bool   `yaml:"use_path_style" json:"use_path_style"`
	// Pointer so an explicit false survives reloads; nil means enabled.
	Enabled *bool `yaml:"enabled" json:"enabled"`
}

func (d BackupDestination) IsEnabled() bool {
	return d.Enabled == nil || *d.Enabled
}

// StoreKind returns the store kind, defaulting to "external" when unset.
func (d BackupDestination) StoreKind() string {
	if d.Kind == "" {
		return "external"
	}
	return d.Kind
}

type AIConfig struct {
	Enabled bool          `yaml:"enabled" json:"enabled"`
	BaseURL string        `yaml:"base_url" json:"base_url"`
	APIKey  string        `yaml:"api_key" json:"api_key"`
	Model   string        `yaml:"model" json:"model"`
	Timeout time.Duration `yaml:"timeout" json:"timeout"`
	DocsURL string        `yaml:"docs_url" json:"docs_url"`
	// TriageDailyCap bounds the log incidents a day the assistant may be asked to explain.
	// Triage runs unattended, so it gets a ceiling that operator-initiated chat does not need.
	TriageDailyCap int `yaml:"triage_daily_cap" json:"triage_daily_cap"`
}

type PlansConfig struct {
	TTL           time.Duration `yaml:"ttl" json:"ttl"`
	RetentionDays int           `yaml:"retention_days" json:"retention_days"`
}

type DomainConfig struct {
	DefaultDomain  string `yaml:"default_domain"`
	AutoSubdomain  bool   `yaml:"auto_subdomain"`
	AutoSSL        bool   `yaml:"auto_ssl"`
	SubdomainStyle string `yaml:"subdomain_style"`
}

type APIConfig struct {
	Host           string   `yaml:"host"`
	Port           int      `yaml:"port"`
	EnableCORS     bool     `yaml:"enable_cors"`
	AllowedOrigins []string `yaml:"allowed_origins"`
}

type AuthConfig struct {
	Enabled   bool     `yaml:"enabled"`
	APIKeys   []string `yaml:"api_keys"`
	JWTSecret string   `yaml:"jwt_secret"`
}

type NginxConfig struct {
	Enabled              bool   `yaml:"enabled" json:"enabled"`
	Image                string `yaml:"image" json:"image"`
	ContainerName        string `yaml:"container_name" json:"container_name"`
	ConfigPath           string `yaml:"config_path" json:"config_path"`
	ReloadCommand        string `yaml:"reload_command" json:"reload_command"`
	External             bool   `yaml:"external" json:"external"`
	ContainerWebrootPath string `yaml:"container_webroot_path" json:"container_webroot_path"`
	RejectUnknownDomains bool   `yaml:"reject_unknown_domains" json:"reject_unknown_domains"`
}

type CertbotConfig struct {
	Enabled              bool          `yaml:"enabled" json:"enabled"`
	Image                string        `yaml:"image" json:"image"`
	Email                string        `yaml:"email" json:"email"`
	Staging              bool          `yaml:"staging" json:"staging"`
	CertsPath            string        `yaml:"certs_path" json:"certs_path"`
	WebrootPath          string        `yaml:"webroot_path" json:"webroot_path"`
	ContainerWebrootPath string        `yaml:"container_webroot_path" json:"container_webroot_path"`
	DNSProvider          string        `yaml:"dns_provider" json:"dns_provider"`
	AutoRenewalEnabled   *bool         `yaml:"auto_renewal_enabled" json:"auto_renewal_enabled"`
	RenewalThresholdDays int           `yaml:"renewal_threshold_days" json:"renewal_threshold_days"`
	RenewalCheckInterval time.Duration `yaml:"renewal_check_interval" json:"renewal_check_interval"`
}

type ServiceExecConfig struct {
	Image        string   `yaml:"image" json:"image"`
	Container    string   `yaml:"container" json:"container"`
	KeepAlive    bool     `yaml:"keep_alive" json:"keep_alive"`
	RunOnRequest bool     `yaml:"run_on_request" json:"run_on_request"`
	Volumes      []string `yaml:"volumes" json:"volumes"`
	Networks     []string `yaml:"networks" json:"networks"`
}

func (c *ServiceExecConfig) ShouldUseDockerRun() bool {
	return !c.KeepAlive && c.RunOnRequest
}

type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type HealthConfig struct {
	CheckInterval    time.Duration `yaml:"check_interval"`
	MetricsRetention time.Duration `yaml:"metrics_retention"`
}

type InfrastructureConfig struct {
	DefaultProxyNetwork    string `yaml:"default_proxy_network" json:"default_proxy_network"`
	DefaultDatabaseNetwork string `yaml:"default_database_network" json:"default_database_network"`
	// DefaultObjectStorageNetwork is the shared network self-hosted object
	// stores join so apps can reach them by name. Empty reuses the database
	// network (object storage is a data backend like a database); set it to run
	// object stores on a dedicated network instead.
	DefaultObjectStorageNetwork string               `yaml:"default_object_storage_network" json:"default_object_storage_network"`
	Database                    SharedDatabaseConfig `yaml:"database" json:"database"`
	Redis                       SharedRedisConfig    `yaml:"redis" json:"redis"`
	PowerDNS                    PowerDNSConfig       `yaml:"powerdns" json:"powerdns"`
}

type PowerDNSConfig struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	Container   string `yaml:"container" json:"container"`
	Image       string `yaml:"image" json:"image"`
	APIPort     int    `yaml:"api_port" json:"api_port"`
	DNSPort     int    `yaml:"dns_port" json:"dns_port"`
	APIKey      string `yaml:"api_key" json:"api_key"`
	DataPath    string `yaml:"data_path" json:"data_path"`
	DefaultSOA  string `yaml:"default_soa" json:"default_soa"`
	Nameservers string `yaml:"nameservers" json:"nameservers"`
}

type SharedDatabaseConfig struct {
	Enabled      bool   `yaml:"enabled" json:"enabled"`
	Type         string `yaml:"type" json:"type"`
	Container    string `yaml:"container" json:"container"`
	Host         string `yaml:"host" json:"host"`
	Port         int    `yaml:"port" json:"port"`
	RootUser     string `yaml:"root_user" json:"root_user"`
	RootPassword string `yaml:"root_password" json:"root_password"`
}

type SharedRedisConfig struct {
	Enabled   bool   `yaml:"enabled" json:"enabled"`
	Container string `yaml:"container" json:"container"`
	Host      string `yaml:"host" json:"host"`
	Port      int    `yaml:"port" json:"port"`
	Password  string `yaml:"password" json:"password"`
}

type SecurityConfig struct {
	Enabled            bool          `yaml:"enabled" json:"enabled"`
	RealtimeCapture    bool          `yaml:"realtime_capture" json:"realtime_capture"`
	ScanInterval       time.Duration `yaml:"scan_interval" json:"scan_interval"`
	RetentionDays      int           `yaml:"retention_days" json:"retention_days"`
	RateThreshold      int           `yaml:"rate_threshold" json:"rate_threshold"`
	AutoBlockEnabled   bool          `yaml:"auto_block_enabled" json:"auto_block_enabled"`
	AutoBlockThreshold int           `yaml:"auto_block_threshold" json:"auto_block_threshold"`
	AutoBlockDuration  time.Duration `yaml:"auto_block_duration" json:"auto_block_duration"`

	// Detection thresholds for autoblock
	DetectionWindow       time.Duration `yaml:"detection_window" json:"detection_window"`
	NotFoundThreshold     int           `yaml:"not_found_threshold" json:"not_found_threshold"`
	AuthFailureThreshold  int           `yaml:"auth_failure_threshold" json:"auth_failure_threshold"`
	UniquePathsThreshold  int           `yaml:"unique_paths_threshold" json:"unique_paths_threshold"`
	RepeatedHitsThreshold int           `yaml:"repeated_hits_threshold" json:"repeated_hits_threshold"`

	// Internal API token for nginx-to-agent communication (auto-generated if empty)
	InternalAPIToken string `yaml:"internal_api_token" json:"-"`

	TrustedProxies []string `yaml:"trusted_proxies" json:"trusted_proxies"`
	TrustCFHeader  bool     `yaml:"trust_cf_header" json:"trust_cf_header"`
}

type AuditConfig struct {
	Enabled            bool          `yaml:"enabled" json:"enabled"`
	RetentionDays      int           `yaml:"retention_days" json:"retention_days"`
	CaptureRequestBody bool          `yaml:"capture_request_body" json:"capture_request_body"`
	ExcludedPaths      []string      `yaml:"excluded_paths" json:"excluded_paths"`
	SensitiveFields    []string      `yaml:"sensitive_fields" json:"sensitive_fields"`
	CleanupInterval    time.Duration `yaml:"cleanup_interval" json:"cleanup_interval"`
}

type SystemTerminalConfig struct {
	ProtectedMode models.ProtectedModeConfig `yaml:"protected_mode" json:"protected_mode"`
}

type CleanupConfig struct {
	Timeout time.Duration `yaml:"timeout" json:"timeout"`
}

func FindConfigPath(providedPath string) string {
	if providedPath != "" && providedPath != "config.yml" {
		return providedPath
	}

	candidates := []string{
		"/etc/flatrun/config.yml",
		"/opt/flatrun/config.yml",
		"./config.yml",
	}

	homeDir, err := os.UserHomeDir()
	if err == nil {
		candidates = append([]string{
			homeDir + "/.config/flatrun/config.yml",
		}, candidates...)
	}

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	if providedPath != "" {
		return providedPath
	}
	return "/etc/flatrun/config.yml"
}

func Load(path string) (*Config, error) {
	actualPath := FindConfigPath(path)
	data, err := os.ReadFile(actualPath)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	setDefaults(&cfg)
	return &cfg, nil
}

func setDefaults(cfg *Config) {
	if cfg.DeploymentsPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cfg.DeploymentsPath = home + "/flatrun/deployments"
		} else {
			cfg.DeploymentsPath = "/var/lib/flatrun/deployments"
		}
	} else if strings.HasPrefix(cfg.DeploymentsPath, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			cfg.DeploymentsPath = home + cfg.DeploymentsPath[1:]
		}
	}
	if cfg.SystemFilesRoot == "" {
		cfg.SystemFilesRoot = "/"
	} else if strings.HasPrefix(cfg.SystemFilesRoot, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			cfg.SystemFilesRoot = home + cfg.SystemFilesRoot[1:]
		}
	}
	if cfg.DockerSocket == "" {
		cfg.DockerSocket = detectDockerHost()
	}
	if cfg.API.Host == "" {
		cfg.API.Host = "0.0.0.0"
	}
	if cfg.API.Port == 0 {
		cfg.API.Port = 8080
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.Format == "" {
		cfg.Logging.Format = "json"
	}
	if cfg.Health.CheckInterval == 0 {
		cfg.Health.CheckInterval = 30 * time.Second
	}
	if cfg.Health.MetricsRetention == 0 {
		cfg.Health.MetricsRetention = 24 * time.Hour
	}
	if cfg.DefaultTimeout == 0 {
		cfg.DefaultTimeout = 2 * time.Minute
	}
	if cfg.Cluster.Orchestrator == "" {
		cfg.Cluster.Orchestrator = "standalone"
	}
	if cfg.Cluster.Routing == "" {
		cfg.Cluster.Routing = "nginx"
	}
	if cfg.Capacity.AllocationThresholdPercent == 0 {
		cfg.Capacity.AllocationThresholdPercent = 90
	}
	if cfg.Capacity.HostThresholdPercent == 0 {
		cfg.Capacity.HostThresholdPercent = 85
	}
	if cfg.Capacity.HostMemoryReserve == 0 {
		cfg.Capacity.HostMemoryReserve = 512 * 1024 * 1024
	}
	if cfg.Capacity.HostCPUReserve == 0 {
		cfg.Capacity.HostCPUReserve = 0.25
	}
	if cfg.Capacity.MemoryStepPercent == 0 {
		cfg.Capacity.MemoryStepPercent = 50
	}
	if cfg.Capacity.CPUStepPercent == 0 {
		cfg.Capacity.CPUStepPercent = 50
	}
	if cfg.Capacity.AllowVertical == nil {
		enabled := true
		cfg.Capacity.AllowVertical = &enabled
	}
	if cfg.Capacity.AllowHorizontal == nil {
		enabled := true
		cfg.Capacity.AllowHorizontal = &enabled
	}
	if cfg.Cleanup.Timeout == 0 {
		cfg.Cleanup.Timeout = cfg.DefaultTimeout
	}
	if cfg.Auth.JWTSecret == "" {
		cfg.Auth.JWTSecret = "default-secret-change-me"
	}
	if cfg.Domain.SubdomainStyle == "" {
		cfg.Domain.SubdomainStyle = "words"
	}
	if cfg.Infrastructure.DefaultProxyNetwork == "" {
		cfg.Infrastructure.DefaultProxyNetwork = "proxy"
	}
	if cfg.Infrastructure.DefaultDatabaseNetwork == "" {
		cfg.Infrastructure.DefaultDatabaseNetwork = "database"
	}
	if cfg.Infrastructure.Database.Port == 0 && cfg.Infrastructure.Database.Enabled {
		switch cfg.Infrastructure.Database.Type {
		case "mysql", "mariadb":
			cfg.Infrastructure.Database.Port = 3306
		case "postgres":
			cfg.Infrastructure.Database.Port = 5432
		default:
			cfg.Infrastructure.Database.Port = 3306
		}
	}
	if cfg.Infrastructure.Database.RootUser == "" && cfg.Infrastructure.Database.Enabled {
		cfg.Infrastructure.Database.RootUser = "root"
	}
	if cfg.Infrastructure.Redis.Port == 0 && cfg.Infrastructure.Redis.Enabled {
		cfg.Infrastructure.Redis.Port = 6379
	}
	if cfg.Templates.GitHub.Enabled == nil {
		enabled := true
		cfg.Templates.GitHub.Enabled = &enabled
	}
	if cfg.Templates.GitHub.Repo == "" {
		cfg.Templates.GitHub.Repo = "flatrun/templates"
	}
	if cfg.Templates.GitHub.Ref == "" {
		cfg.Templates.GitHub.Ref = "main"
	}
	if cfg.Templates.SyncInterval == nil {
		defaultInterval := 3600
		cfg.Templates.SyncInterval = &defaultInterval
	}
	if cfg.Nginx.Image == "" {
		cfg.Nginx.Image = "nginx:alpine"
	}
	if cfg.Nginx.ContainerName == "" {
		cfg.Nginx.ContainerName = "nginx"
	}
	if cfg.Certbot.Image == "" {
		cfg.Certbot.Image = "certbot/certbot"
	}
	if cfg.Certbot.RenewalThresholdDays == 0 {
		cfg.Certbot.RenewalThresholdDays = 30
	}
	if cfg.Certbot.RenewalCheckInterval == 0 {
		cfg.Certbot.RenewalCheckInterval = 12 * time.Hour
	}
	// Auto-renewal defaults on: an unset flag should renew certificates before they
	// expire, not leave them to lapse. An explicit false is still honored.
	if cfg.Certbot.AutoRenewalEnabled == nil {
		enabled := true
		cfg.Certbot.AutoRenewalEnabled = &enabled
	}
	// Security defaults
	if cfg.Security.ScanInterval == 0 {
		cfg.Security.ScanInterval = 30 * time.Second
	}
	if cfg.Security.RetentionDays == 0 {
		cfg.Security.RetentionDays = 30
	}
	if cfg.Security.RateThreshold == 0 {
		cfg.Security.RateThreshold = 100
	}
	if cfg.Security.AutoBlockThreshold == 0 {
		cfg.Security.AutoBlockThreshold = 50
	}
	if cfg.Security.AutoBlockDuration == 0 {
		cfg.Security.AutoBlockDuration = 24 * time.Hour
	}
	// Detection threshold defaults
	if cfg.Security.DetectionWindow == 0 {
		cfg.Security.DetectionWindow = 2 * time.Minute
	}
	if cfg.Security.NotFoundThreshold == 0 {
		cfg.Security.NotFoundThreshold = 10
	}
	if cfg.Security.AuthFailureThreshold == 0 {
		cfg.Security.AuthFailureThreshold = 5
	}
	if cfg.Security.UniquePathsThreshold == 0 {
		cfg.Security.UniquePathsThreshold = 20
	}
	if cfg.Security.RepeatedHitsThreshold == 0 {
		cfg.Security.RepeatedHitsThreshold = 30
	}
	if cfg.Security.InternalAPIToken == "" {
		bytes := make([]byte, 32)
		if _, err := rand.Read(bytes); err == nil {
			cfg.Security.InternalAPIToken = hex.EncodeToString(bytes)
		}
	}
	// PowerDNS defaults
	if cfg.Infrastructure.PowerDNS.Container == "" {
		cfg.Infrastructure.PowerDNS.Container = "powerdns"
	}
	if cfg.Infrastructure.PowerDNS.Image == "" {
		cfg.Infrastructure.PowerDNS.Image = "powerdns/pdns-auth-48:latest"
	}
	if cfg.Infrastructure.PowerDNS.APIPort == 0 {
		cfg.Infrastructure.PowerDNS.APIPort = 8081
	}
	if cfg.Infrastructure.PowerDNS.DNSPort == 0 {
		cfg.Infrastructure.PowerDNS.DNSPort = 53
	}
	if cfg.Infrastructure.PowerDNS.APIKey == "" {
		bytes := make([]byte, 24)
		if _, err := rand.Read(bytes); err == nil {
			cfg.Infrastructure.PowerDNS.APIKey = hex.EncodeToString(bytes)
		}
	}
	// Audit defaults
	if cfg.Audit.RetentionDays == 0 {
		cfg.Audit.RetentionDays = 30
	}
	if cfg.Audit.CleanupInterval == 0 {
		cfg.Audit.CleanupInterval = 24 * time.Hour
	}
	if cfg.Audit.ExcludedPaths == nil {
		cfg.Audit.ExcludedPaths = []string{"/api/health"}
	}
	if cfg.Audit.SensitiveFields == nil {
		cfg.Audit.SensitiveFields = []string{"password", "token", "secret", "api_key", "authorization"}
	}
	// AI defaults
	if cfg.AI.BaseURL == "" {
		cfg.AI.BaseURL = "https://api.openai.com/v1"
	}
	if cfg.AI.Timeout == 0 {
		cfg.AI.Timeout = 60 * time.Second
	}
	if cfg.AI.DocsURL == "" {
		cfg.AI.DocsURL = "https://flatrun.dev/docs/"
	}
	// Plans defaults
	if cfg.Plans.TTL == 0 {
		cfg.Plans.TTL = 24 * time.Hour
	}
	if cfg.Plans.RetentionDays == 0 {
		cfg.Plans.RetentionDays = 30
	}
	// Files defaults
	if cfg.Files.ShowHidden == nil {
		showHidden := true
		cfg.Files.ShowHidden = &showHidden
	}
	// Cluster defaults
	if cfg.Cluster.ServerName == "" {
		hostname, err := os.Hostname()
		if err != nil {
			hostname = "flatrun-agent"
		}
		cfg.Cluster.ServerName = hostname
	}
	if cfg.Cluster.HealthInterval == "" {
		cfg.Cluster.HealthInterval = "30s"
	}
	if cfg.Cluster.RequestTimeout == "" {
		cfg.Cluster.RequestTimeout = "10s"
	}
}

func detectDockerHost() string {
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		return host
	}
	if host := dockerContextHost(); host != "" {
		return host
	}
	if runtime.GOOS == "windows" {
		return "npipe:////./pipe/docker_engine"
	}
	return "unix:///var/run/docker.sock"
}

func dockerContextHost() string {
	cmd := exec.Command("docker", "context", "inspect",
		"--format", "{{.Endpoints.docker.Host}}")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func Save(cfg *Config, path string) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
