package config

import (
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
	Enabled       bool   `yaml:"enabled" json:"enabled"`
	Image         string `yaml:"image" json:"image"`
	ContainerName string `yaml:"container_name" json:"container_name"`
	ConfigPath    string `yaml:"config_path" json:"config_path"`
	ReloadCommand string `yaml:"reload_command" json:"reload_command"`
	External      bool   `yaml:"external" json:"external"`
}

type CertbotConfig struct {
	Enabled       bool   `yaml:"enabled" json:"enabled"`
	Image         string `yaml:"image" json:"image"`
	ContainerName string `yaml:"container_name" json:"container_name"`
	Email         string `yaml:"email" json:"email"`
	Staging       bool   `yaml:"staging" json:"staging"`
	CertsPath     string `yaml:"certs_path" json:"certs_path"`
	WebrootPath   string `yaml:"webroot_path" json:"webroot_path"`
	DNSProvider   string `yaml:"dns_provider" json:"dns_provider"`
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

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
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
}

func Save(cfg *Config, path string) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
