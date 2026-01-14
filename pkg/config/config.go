package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	DeploymentsPath string               `yaml:"deployments_path"`
	DockerSocket    string               `yaml:"docker_socket"`
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
	Enabled              bool   `yaml:"enabled" json:"enabled"`
	Image                string `yaml:"image" json:"image"`
	Email                string `yaml:"email" json:"email"`
	Staging              bool   `yaml:"staging" json:"staging"`
	CertsPath            string `yaml:"certs_path" json:"certs_path"`
	WebrootPath          string `yaml:"webroot_path" json:"webroot_path"`
	ContainerWebrootPath string `yaml:"container_webroot_path" json:"container_webroot_path"`
	DNSProvider          string `yaml:"dns_provider" json:"dns_provider"`
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
	DefaultProxyNetwork    string               `yaml:"default_proxy_network" json:"default_proxy_network"`
	DefaultDatabaseNetwork string               `yaml:"default_database_network" json:"default_database_network"`
	Database               SharedDatabaseConfig `yaml:"database" json:"database"`
	Redis                  SharedRedisConfig    `yaml:"redis" json:"redis"`
	PowerDNS               PowerDNSConfig       `yaml:"powerdns" json:"powerdns"`
}

type PowerDNSConfig struct {
	Enabled       bool   `yaml:"enabled" json:"enabled"`
	Container     string `yaml:"container" json:"container"`
	Image         string `yaml:"image" json:"image"`
	APIPort       int    `yaml:"api_port" json:"api_port"`
	DNSPort       int    `yaml:"dns_port" json:"dns_port"`
	APIKey        string `yaml:"api_key" json:"api_key"`
	DataPath      string `yaml:"data_path" json:"data_path"`
	DefaultSOA    string `yaml:"default_soa" json:"default_soa"`
	Nameservers   string `yaml:"nameservers" json:"nameservers"`
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
}

type AuditConfig struct {
	Enabled            bool          `yaml:"enabled" json:"enabled"`
	RetentionDays      int           `yaml:"retention_days" json:"retention_days"`
	CaptureRequestBody bool          `yaml:"capture_request_body" json:"capture_request_body"`
	ExcludedPaths      []string      `yaml:"excluded_paths" json:"excluded_paths"`
	SensitiveFields    []string      `yaml:"sensitive_fields" json:"sensitive_fields"`
	CleanupInterval    time.Duration `yaml:"cleanup_interval" json:"cleanup_interval"`
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
		cfg.DeploymentsPath = "/deployments"
	}
	if cfg.DockerSocket == "" {
		cfg.DockerSocket = "unix:///var/run/docker.sock"
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
	if cfg.Nginx.Image == "" {
		cfg.Nginx.Image = "nginx:alpine"
	}
	if cfg.Nginx.ContainerName == "" {
		cfg.Nginx.ContainerName = "nginx"
	}
	if cfg.Certbot.Image == "" {
		cfg.Certbot.Image = "certbot/certbot"
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
}

func Save(cfg *Config, path string) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
