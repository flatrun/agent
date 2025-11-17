package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	DeploymentsPath string        `yaml:"deployments_path"`
	DockerSocket    string        `yaml:"docker_socket"`
	API             APIConfig     `yaml:"api"`
	Auth            AuthConfig    `yaml:"auth"`
	Nginx           NginxConfig   `yaml:"nginx"`
	Certbot         CertbotConfig `yaml:"certbot"`
	Logging         LoggingConfig `yaml:"logging"`
	Health          HealthConfig  `yaml:"health"`
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
	ContainerName string `yaml:"container_name"`
	ConfigPath    string `yaml:"config_path"`
	ReloadCommand string `yaml:"reload_command"`
}

type CertbotConfig struct {
	ContainerName string `yaml:"container_name"`
	Email         string `yaml:"email"`
	Staging       bool   `yaml:"staging"`
}

type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type HealthConfig struct {
	CheckInterval    time.Duration `yaml:"check_interval"`
	MetricsRetention time.Duration `yaml:"metrics_retention"`
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
}
