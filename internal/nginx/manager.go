package nginx

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"text/template"

	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
)

type Manager struct {
	config               *config.NginxConfig
	basePath             string
	configPath           string
	webrootPath          string
	containerWebrootPath string
	mu                   sync.RWMutex
}

func NewManager(cfg *config.NginxConfig, deploymentsPath string, webrootPath string) *Manager {
	configPath := cfg.ConfigPath
	if configPath == "" {
		configPath = filepath.Join(deploymentsPath, "nginx", "conf.d")
	}

	if webrootPath == "" {
		webrootPath = filepath.Join(deploymentsPath, "nginx", "html")
	}

	containerWebrootPath := cfg.ContainerWebrootPath
	if containerWebrootPath == "" {
		containerWebrootPath = "/usr/share/nginx/html"
	}

	return &Manager{
		config:               cfg,
		basePath:             deploymentsPath,
		configPath:           configPath,
		webrootPath:          webrootPath,
		containerWebrootPath: containerWebrootPath,
	}
}

func (m *Manager) ConfigPath() string {
	return m.configPath
}

func (m *Manager) UpdateConfig(cfg *config.NginxConfig, deploymentsPath string, webrootPath string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.config = cfg

	configPath := cfg.ConfigPath
	if configPath == "" {
		configPath = filepath.Join(deploymentsPath, "nginx", "conf.d")
	}
	m.configPath = configPath

	if webrootPath == "" {
		webrootPath = filepath.Join(deploymentsPath, "nginx", "html")
	}
	m.webrootPath = webrootPath

	containerWebrootPath := cfg.ContainerWebrootPath
	if containerWebrootPath == "" {
		containerWebrootPath = "/usr/share/nginx/html"
	}
	m.containerWebrootPath = containerWebrootPath

	m.basePath = deploymentsPath
}

func (m *Manager) CreateVirtualHost(deployment *models.Deployment) error {
	if deployment.Metadata == nil {
		return fmt.Errorf("deployment has no metadata")
	}

	if !deployment.Metadata.Networking.Expose {
		return nil
	}

	if deployment.Metadata.Networking.Domain == "" {
		return fmt.Errorf("domain is required for exposed deployments")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.MkdirAll(m.configPath, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	configContent, err := m.generateConfig(deployment)
	if err != nil {
		return fmt.Errorf("failed to generate nginx config: %w", err)
	}

	configFile := filepath.Join(m.configPath, deployment.Name+".conf")
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		return fmt.Errorf("failed to write nginx config: %w", err)
	}

	return nil
}

func (m *Manager) DeleteVirtualHost(deploymentName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	configFile := filepath.Join(m.configPath, deploymentName+".conf")
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		return nil
	}

	return os.Remove(configFile)
}

func (m *Manager) GetVirtualHost(deploymentName string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	configFile := filepath.Join(m.configPath, deploymentName+".conf")
	data, err := os.ReadFile(configFile)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

func (m *Manager) UpdateVirtualHost(deployment *models.Deployment) error {
	return m.CreateVirtualHost(deployment)
}

func (m *Manager) VirtualHostExists(deploymentName string) bool {
	configFile := filepath.Join(m.configPath, deploymentName+".conf")
	_, err := os.Stat(configFile)
	return err == nil
}

func (m *Manager) ListVirtualHosts() ([]VirtualHostInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var hosts []VirtualHostInfo

	entries, err := os.ReadDir(m.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return hosts, nil
		}
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".conf") {
			continue
		}

		name := strings.TrimSuffix(entry.Name(), ".conf")
		info, _ := entry.Info()

		hosts = append(hosts, VirtualHostInfo{
			Name:       name,
			ConfigFile: filepath.Join(m.configPath, entry.Name()),
			ModifiedAt: info.ModTime().Unix(),
		})
	}

	return hosts, nil
}

func (m *Manager) Reload() error {
	if m.config.ContainerName == "" {
		return fmt.Errorf("nginx container name not configured")
	}

	reloadCmd := m.config.ReloadCommand
	if reloadCmd == "" {
		reloadCmd = "nginx -s reload"
	}

	cmd := exec.Command("docker", "exec", m.config.ContainerName, "sh", "-c", reloadCmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to reload nginx: %s - %w", string(output), err)
	}

	return nil
}

func (m *Manager) TestConfig() error {
	if m.config.ContainerName == "" {
		return fmt.Errorf("nginx container name not configured")
	}

	cmd := exec.Command("docker", "exec", m.config.ContainerName, "nginx", "-t")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nginx config test failed: %s - %w", string(output), err)
	}

	return nil
}

func (m *Manager) generateConfig(deployment *models.Deployment) (string, error) {
	net := deployment.Metadata.Networking
	ssl := deployment.Metadata.SSL

	data := templateData{
		DeploymentName:       deployment.Name,
		Domain:               net.Domain,
		ContainerPort:        net.ContainerPort,
		Protocol:             net.Protocol,
		ProxyType:            net.ProxyType,
		SSLEnabled:           ssl.Enabled,
		HealthPath:           deployment.Metadata.HealthCheck.Path,
		ContainerWebrootPath: m.containerWebrootPath,
	}

	if data.ContainerPort == 0 {
		data.ContainerPort = 80
	}
	if data.Protocol == "" {
		data.Protocol = "http"
	}
	if data.ProxyType == "" {
		data.ProxyType = "http"
	}

	var tmpl *template.Template
	var err error

	if data.SSLEnabled {
		tmpl, err = template.New("nginx").Parse(sslTemplate)
	} else {
		tmpl, err = template.New("nginx").Parse(httpTemplate)
	}

	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

type VirtualHostInfo struct {
	Name       string `json:"name"`
	ConfigFile string `json:"config_file"`
	ModifiedAt int64  `json:"modified_at"`
}

type templateData struct {
	DeploymentName       string
	Domain               string
	ContainerPort        int
	Protocol             string
	ProxyType            string
	SSLEnabled           bool
	HealthPath           string
	ContainerWebrootPath string
}

const httpTemplate = `server {
    listen 80;
    server_name {{.Domain}};

    resolver 127.0.0.11 valid=30s ipv6=off;

    location / {
        set $upstream {{.DeploymentName}}:{{.ContainerPort}};
        proxy_pass {{.Protocol}}://$upstream;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_connect_timeout 60s;
        proxy_send_timeout 60s;
        proxy_read_timeout 60s;
    }
{{if .HealthPath}}
    location {{.HealthPath}} {
        set $upstream {{.DeploymentName}}:{{.ContainerPort}};
        proxy_pass {{.Protocol}}://$upstream{{.HealthPath}};
        proxy_set_header Host $host;
    }
{{end}}
    location /.well-known/acme-challenge/ {
        root {{.ContainerWebrootPath}};
    }
}
`

const sslTemplate = `server {
    listen 80;
    server_name {{.Domain}};

    location /.well-known/acme-challenge/ {
        root {{.ContainerWebrootPath}};
    }

    location / {
        return 301 https://$host$request_uri;
    }
}

server {
    listen 443 ssl;
    http2 on;
    server_name {{.Domain}};

    ssl_certificate /etc/nginx/certs/live/{{.Domain}}/fullchain.pem;
    ssl_certificate_key /etc/nginx/certs/live/{{.Domain}}/privkey.pem;

    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384;
    ssl_prefer_server_ciphers off;
    ssl_session_timeout 1d;
    ssl_session_cache shared:SSL:50m;
    ssl_stapling on;
    ssl_stapling_verify on;

    add_header Strict-Transport-Security "max-age=63072000" always;

    resolver 127.0.0.11 valid=30s ipv6=off;

    location / {
        set $upstream {{.DeploymentName}}:{{.ContainerPort}};
        proxy_pass {{.Protocol}}://$upstream;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_connect_timeout 60s;
        proxy_send_timeout 60s;
        proxy_read_timeout 60s;
    }
{{if .HealthPath}}
    location {{.HealthPath}} {
        set $upstream {{.DeploymentName}}:{{.ContainerPort}};
        proxy_pass {{.Protocol}}://$upstream{{.HealthPath}};
        proxy_set_header Host $host;
    }
{{end}}
}
`
