package nginx

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
)

type Manager struct {
	config               *config.NginxConfig
	basePath             string
	configPath           string
	webrootPath          string
	containerWebrootPath string
	staplingChecker      func(sslDomain string) bool
	mu                   sync.RWMutex
}

// mapsConfigFile is a managed http-context snippet (not a vhost) included
// ahead of the generated vhosts. It defines $connection_upgrade so the
// Connection header is only sent when a client requests a WebSocket upgrade.
const mapsConfigFile = "00-flatrun-maps.conf"

const mapsConfigContent = `# Managed by FlatRun. Do not edit.
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}
`

// infraConfigFiles are conf.d files the agent manages that are not deployment
// virtual hosts and must be skipped when enumerating vhosts.
var infraConfigFiles = map[string]bool{
	mapsConfigFile:     true,
	"rate_limits.conf": true,
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

// SetStaplingChecker injects a predicate that reports whether ssl_stapling
// should be enabled for an SSL domain (true when its certificate advertises an
// OCSP responder). When left unset, stapling stays enabled, preserving prior
// behaviour.
func (m *Manager) SetStaplingChecker(fn func(sslDomain string) bool) {
	m.staplingChecker = fn
}

func (m *Manager) shouldStaple(sslDomain string) bool {
	if m.staplingChecker == nil {
		return true
	}
	return m.staplingChecker(sslDomain)
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

	domains := deployment.Metadata.GetDomains()
	if len(domains) == 0 {
		return nil
	}

	return m.CreateMultiDomainVirtualHost(deployment)
}

func (m *Manager) DeleteVirtualHost(deploymentName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	configFile := filepath.Join(m.configPath, deploymentName+".conf")
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		return nil
	}

	// Remove deployment rate limits
	_ = m.updateRateLimitsInternal(deploymentName, nil)

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

func (m *Manager) WriteVirtualHost(deploymentName string, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureMapsConfig(); err != nil {
		return err
	}

	configFile := filepath.Join(m.configPath, deploymentName+".conf")
	return os.WriteFile(configFile, []byte(content), 0644)
}

// ensureMapsConfig writes the managed http-context map snippet that generated
// vhosts depend on for conditional WebSocket upgrades. It is idempotent.
// Callers must hold m.mu.
func (m *Manager) ensureMapsConfig() error {
	if err := os.MkdirAll(m.configPath, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	path := filepath.Join(m.configPath, mapsConfigFile)
	if err := os.WriteFile(path, []byte(mapsConfigContent), 0644); err != nil {
		return fmt.Errorf("failed to write maps config: %w", err)
	}
	return nil
}

func (m *Manager) UpdateVirtualHost(deployment *models.Deployment) error {
	return m.CreateVirtualHost(deployment)
}

// RenderVirtualHost returns the config that CreateVirtualHost would
// write, without touching disk or nginx.
func (m *Manager) RenderVirtualHost(deployment *models.Deployment) (string, error) {
	if deployment.Metadata == nil {
		return "", fmt.Errorf("deployment has no metadata")
	}
	if len(deployment.Metadata.GetDomains()) == 0 {
		return "", nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.generateMultiDomainConfig(deployment)
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
		if infraConfigFiles[entry.Name()] {
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

func (m *Manager) GetVhostsUsingSSLDomain(domain string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var vhosts []string

	entries, err := os.ReadDir(m.configPath)
	if err != nil {
		return vhosts
	}

	certPath := fmt.Sprintf("/etc/letsencrypt/live/%s/", domain)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".conf") {
			continue
		}

		configPath := filepath.Join(m.configPath, entry.Name())
		content, err := os.ReadFile(configPath)
		if err != nil {
			continue
		}

		if strings.Contains(string(content), certPath) {
			vhosts = append(vhosts, strings.TrimSuffix(entry.Name(), ".conf"))
		}
	}

	return vhosts
}

func (m *Manager) Reload() error {
	if m.config.ContainerName == "" {
		return fmt.Errorf("nginx container name not configured")
	}

	if err := m.waitForContainerReady(5); err != nil {
		return fmt.Errorf("container not ready: %w", err)
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

	if err := m.waitForContainerReady(5); err != nil {
		return fmt.Errorf("container not ready: %w", err)
	}

	cmd := exec.Command("docker", "exec", m.config.ContainerName, "nginx", "-t")
	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := string(output)
		if isNginxConfigValid(outputStr) {
			log.Printf("nginx config test passed with warnings: %s", outputStr)
			return nil
		}
		return fmt.Errorf("nginx config test failed: %s - %w", outputStr, err)
	}

	return nil
}

func isNginxConfigValid(output string) bool {
	hasError := strings.Contains(output, "[emerg]") || strings.Contains(output, "[error]")
	hasSuccess := strings.Contains(output, "syntax is ok") || strings.Contains(output, "test is successful")
	return !hasError && hasSuccess
}

func (m *Manager) waitForContainerReady(maxRetries int) error {
	for i := 0; i < maxRetries; i++ {
		cmd := exec.Command("docker", "inspect", "-f", "{{.State.Status}}", m.config.ContainerName)
		output, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("failed to get container status: %w", err)
		}

		status := strings.TrimSpace(string(output))
		if status == "running" {
			// Also check it's not in a restart loop
			cmd = exec.Command("docker", "inspect", "-f", "{{.State.Restarting}}", m.config.ContainerName)
			output, err = cmd.Output()
			if err == nil && strings.TrimSpace(string(output)) == "false" {
				return nil
			}
		}

		if i < maxRetries-1 {
			time.Sleep(time.Second)
		}
	}
	return fmt.Errorf("container not ready after %d attempts", maxRetries)
}

func (m *Manager) generateConfig(deployment *models.Deployment) (string, error) {
	net := deployment.Metadata.Networking
	ssl := deployment.Metadata.SSL

	healthPath := deployment.Metadata.HealthCheck.Path
	if healthPath == "/" {
		healthPath = ""
	}

	securityEnabled := false
	var blockedIPs []string
	var rateLimits []rateLimitData

	if deployment.Metadata.Security != nil && deployment.Metadata.Security.Enabled {
		securityEnabled = true
		blockedIPs = deployment.Metadata.Security.BlockedIPs

		for _, rl := range deployment.Metadata.Security.RateLimits {
			if !rl.Enabled {
				continue
			}
			zone := fmt.Sprintf("%s_%s", deployment.Name, sanitizeZoneName(rl.Path))
			burst := rl.Burst
			if burst <= 0 {
				burst = rl.Rate / 2
				if burst < 1 {
					burst = 1
				}
			}
			rateLimits = append(rateLimits, rateLimitData{
				Path:  rl.Path,
				Zone:  zone,
				Rate:  rl.Rate,
				Burst: burst,
			})
		}
	}

	data := templateData{
		DeploymentName:       deployment.Name,
		Domain:               net.Domain,
		ContainerPort:        net.ContainerPort,
		Protocol:             net.Protocol,
		ProxyType:            net.ProxyType,
		SSLEnabled:           ssl.Enabled,
		HealthPath:           healthPath,
		ContainerWebrootPath: m.containerWebrootPath,
		SecurityEnabled:      securityEnabled,
		BlockedIPs:           blockedIPs,
		RateLimits:           rateLimits,
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
	if data.ProxyTimeout == 0 {
		data.ProxyTimeout = defaultProxyTimeout
	}
	if data.SSLEnabled {
		data.EnableStapling = m.shouldStaple(data.Domain)
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

func (m *Manager) generateMultiDomainConfig(deployment *models.Deployment) (string, error) {
	domains := deployment.Metadata.GetDomains()
	if len(domains) == 0 {
		return "", fmt.Errorf("no domains configured")
	}

	securityEnabled := false
	var blockedIPs []string
	var rateLimits []rateLimitData
	if deployment.Metadata.Security != nil && deployment.Metadata.Security.Enabled {
		securityEnabled = true
		blockedIPs = deployment.Metadata.Security.BlockedIPs

		for _, rl := range deployment.Metadata.Security.RateLimits {
			if !rl.Enabled {
				continue
			}
			zone := fmt.Sprintf("%s_%s", deployment.Name, sanitizeZoneName(rl.Path))
			burst := rl.Burst
			if burst <= 0 {
				burst = rl.Rate / 2
				if burst < 1 {
					burst = 1
				}
			}
			rateLimits = append(rateLimits, rateLimitData{
				Path:  rl.Path,
				Zone:  zone,
				Rate:  rl.Rate,
				Burst: burst,
			})
		}
	}

	servers := m.groupDomainsByHost(domains, deployment.Name)

	data := multiRouteTemplateData{
		DeploymentName:       deployment.Name,
		ContainerWebrootPath: m.containerWebrootPath,
		SecurityEnabled:      securityEnabled,
		BlockedIPs:           blockedIPs,
		RateLimits:           rateLimits,
		Servers:              servers,
	}

	allSSL := true
	anySSL := false
	for _, server := range servers {
		if server.HasSSL {
			anySSL = true
		} else {
			allSSL = false
		}
	}

	var tmplStr string
	if allSSL {
		tmplStr = multiRouteSSLTemplate
	} else if anySSL {
		tmplStr = multiRouteMixedTemplate
	} else {
		tmplStr = multiRouteHTTPTemplate
	}

	tmpl, err := template.New("nginx-multi").Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("failed to parse multi-domain template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute multi-domain template: %w", err)
	}

	return buf.String(), nil
}

func (m *Manager) groupDomainsByHost(domains []models.DomainConfig, deploymentName string) []serverData {
	hostDomains := make(map[string][]models.DomainConfig)
	for _, d := range domains {
		hostDomains[d.Domain] = append(hostDomains[d.Domain], d)
	}

	var servers []serverData
	for host, domainList := range hostDomains {
		sort.Slice(domainList, func(i, j int) bool {
			return len(domainList[i].PathPrefix) > len(domainList[j].PathPrefix)
		})

		var locations []locationData
		seenPaths := make(map[string]bool)
		hasSSL := false
		sslDomain := host
		var serverAliases []string

		for _, d := range domainList {
			path := d.PathPrefix
			if path == "" {
				path = "/"
			}

			if seenPaths[path] {
				log.Printf("warning: skipping duplicate location %q for host %q", path, host)
				continue
			}
			seenPaths[path] = true

			service := d.Service
			if service == "" {
				log.Printf("[proxy] warning: domain %q has no service set for deployment %q, falling back to deployment name", d.Domain, deploymentName)
				service = deploymentName
			}

			port := d.ContainerPort
			if port == 0 {
				port = 80
			}

			timeout := d.ProxyTimeout
			if timeout == 0 {
				timeout = defaultProxyTimeout
			}

			locations = append(locations, locationData{
				Path:          path,
				Service:       service,
				ContainerPort: port,
				Protocol:      "http",
				StripPrefix:   d.StripPrefix,
				OriginalPath:  d.PathPrefix,
				ProxyTimeout:  timeout,
			})

			if d.SSL.Enabled {
				hasSSL = true
			}

			for _, alias := range d.Aliases {
				if alias != host {
					serverAliases = append(serverAliases, alias)
				}
			}
		}

		servers = append(servers, serverData{
			Domain:         host,
			SSLEnabled:     hasSSL,
			HasSSL:         hasSSL,
			SSLDomain:      sslDomain,
			Locations:      locations,
			ServerAliases:  serverAliases,
			EnableStapling: hasSSL && m.shouldStaple(sslDomain),
		})
	}

	sort.Slice(servers, func(i, j int) bool {
		return servers[i].Domain < servers[j].Domain
	})

	return servers
}

func (m *Manager) CreateMultiDomainVirtualHost(deployment *models.Deployment) error {
	if deployment.Metadata == nil {
		return fmt.Errorf("deployment has no metadata")
	}

	domains := deployment.Metadata.GetDomains()
	if len(domains) == 0 {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureMapsConfig(); err != nil {
		return err
	}

	configContent, err := m.generateMultiDomainConfig(deployment)
	if err != nil {
		return fmt.Errorf("failed to generate multi-domain nginx config: %w", err)
	}

	configFile := filepath.Join(m.configPath, deployment.Name+".conf")
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		return fmt.Errorf("failed to write nginx config: %w", err)
	}

	if deployment.Metadata.Security != nil && deployment.Metadata.Security.Enabled {
		if err := m.updateRateLimitsInternal(deployment.Name, deployment.Metadata.Security.RateLimits); err != nil {
			return fmt.Errorf("failed to update rate limits: %w", err)
		}
	}

	return nil
}

type VirtualHostInfo struct {
	Name       string `json:"name"`
	ConfigFile string `json:"config_file"`
	ModifiedAt int64  `json:"modified_at"`
}

// defaultProxyTimeout is the proxy read/send timeout in seconds applied when a
// domain does not set one explicitly.
const defaultProxyTimeout = 60

type templateData struct {
	DeploymentName       string
	Domain               string
	ContainerPort        int
	Protocol             string
	ProxyType            string
	SSLEnabled           bool
	HealthPath           string
	ContainerWebrootPath string
	SecurityEnabled      bool
	BlockedIPs           []string
	RateLimits           []rateLimitData
	ProxyTimeout         int
	EnableStapling       bool
}

type multiRouteTemplateData struct {
	DeploymentName       string
	ContainerWebrootPath string
	SecurityEnabled      bool
	BlockedIPs           []string
	RateLimits           []rateLimitData
	Servers              []serverData
}

type serverData struct {
	Domain         string
	SSLEnabled     bool
	Locations      []locationData
	HasSSL         bool
	SSLDomain      string
	ServerAliases  []string
	EnableStapling bool
}

type locationData struct {
	Path          string
	Service       string
	ContainerPort int
	Protocol      string
	StripPrefix   bool
	OriginalPath  string
	ProxyTimeout  int
}

type rateLimitData struct {
	Path  string
	Zone  string
	Rate  int
	Burst int
}

const httpTemplate = `server {
    listen 80;
    server_name {{.Domain}};

    resolver 127.0.0.11 valid=30s ipv6=off;
{{- range .BlockedIPs}}
    deny {{.}};
{{- end}}

    location / {
        set $upstream {{.DeploymentName}}:{{.ContainerPort}};
        proxy_pass {{.Protocol}}://$upstream;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_connect_timeout 60s;
        proxy_send_timeout {{.ProxyTimeout}}s;
        proxy_read_timeout {{.ProxyTimeout}}s;
{{- if .SecurityEnabled}}
        log_by_lua_block {
            security.capture_event()
        }
{{- end}}
    }
{{if .HealthPath}}
    location {{.HealthPath}} {
        set $upstream {{.DeploymentName}}:{{.ContainerPort}};
        proxy_pass {{.Protocol}}://$upstream{{.HealthPath}};
        proxy_set_header Host $host;
    }
{{end}}
{{- range .RateLimits}}
    location {{.Path}} {
        limit_req zone={{.Zone}} burst={{.Burst}} nodelay;
        limit_req_status 429;
        set $upstream {{$.DeploymentName}}:{{$.ContainerPort}};
        proxy_pass {{$.Protocol}}://$upstream;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
{{- if $.SecurityEnabled}}
        log_by_lua_block {
            security.capture_event()
        }
{{- end}}
    }
{{- end}}
    location /.well-known/acme-challenge/ {
        root {{.ContainerWebrootPath}};
        access_log off;
        log_not_found off;
    }
}
`

const sslTemplate = `server {
    listen 80;
    server_name {{.Domain}};

    location /.well-known/acme-challenge/ {
        root {{.ContainerWebrootPath}};
        access_log off;
        log_not_found off;
    }

    location / {
        return 301 https://$host$request_uri;
    }
}

server {
    listen 443 ssl;
    http2 on;
    server_name {{.Domain}};

    ssl_certificate /etc/letsencrypt/live/{{.Domain}}/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/{{.Domain}}/privkey.pem;

    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384;
    ssl_prefer_server_ciphers off;
    ssl_session_timeout 1d;
    ssl_session_cache shared:SSL:50m;
{{- if .EnableStapling}}
    ssl_stapling on;
    ssl_stapling_verify on;
{{- end}}

    add_header Strict-Transport-Security "max-age=63072000" always;

    resolver 127.0.0.11 valid=30s ipv6=off;
{{- range .BlockedIPs}}
    deny {{.}};
{{- end}}

    location / {
        set $upstream {{.DeploymentName}}:{{.ContainerPort}};
        proxy_pass {{.Protocol}}://$upstream;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_connect_timeout 60s;
        proxy_send_timeout {{.ProxyTimeout}}s;
        proxy_read_timeout {{.ProxyTimeout}}s;
{{- if .SecurityEnabled}}
        log_by_lua_block {
            security.capture_event()
        }
{{- end}}
    }
{{if .HealthPath}}
    location {{.HealthPath}} {
        set $upstream {{.DeploymentName}}:{{.ContainerPort}};
        proxy_pass {{.Protocol}}://$upstream{{.HealthPath}};
        proxy_set_header Host $host;
    }
{{end}}
{{- range .RateLimits}}
    location {{.Path}} {
        limit_req zone={{.Zone}} burst={{.Burst}} nodelay;
        limit_req_status 429;
        set $upstream {{$.DeploymentName}}:{{$.ContainerPort}};
        proxy_pass {{$.Protocol}}://$upstream;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
{{- if $.SecurityEnabled}}
        log_by_lua_block {
            security.capture_event()
        }
{{- end}}
    }
{{- end}}
}
`

const multiRouteHTTPTemplate = `{{- range .Servers}}
server {
    listen 80;
    server_name {{.Domain}}{{range .ServerAliases}} {{.}}{{end}};

    resolver 127.0.0.11 valid=30s ipv6=off;
{{- range $.BlockedIPs}}
    deny {{.}};
{{- end}}
{{- range .Locations}}

    location {{.Path}} {
        set $upstream {{.Service}}:{{.ContainerPort}};
{{- if .StripPrefix}}
        rewrite ^{{.OriginalPath}}(.*)$ /$1 break;
{{- end}}
        proxy_pass {{.Protocol}}://$upstream;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_connect_timeout 60s;
        proxy_send_timeout {{.ProxyTimeout}}s;
        proxy_read_timeout {{.ProxyTimeout}}s;
{{- if $.SecurityEnabled}}
        log_by_lua_block {
            security.capture_event()
        }
{{- end}}
    }
{{- end}}
{{- range $.RateLimits}}

    location {{.Path}} {
        limit_req zone={{.Zone}} burst={{.Burst}} nodelay;
        limit_req_status 429;
        set $upstream {{$.DeploymentName}}:80;
        proxy_pass http://$upstream;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
{{- if $.SecurityEnabled}}
        log_by_lua_block {
            security.capture_event()
        }
{{- end}}
    }
{{- end}}

    location /.well-known/acme-challenge/ {
        root {{$.ContainerWebrootPath}};
        access_log off;
        log_not_found off;
    }
}
{{end}}`

const multiRouteSSLTemplate = `{{- range .Servers}}
server {
    listen 80;
    server_name {{.Domain}}{{range .ServerAliases}} {{.}}{{end}};

    location /.well-known/acme-challenge/ {
        root {{$.ContainerWebrootPath}};
        access_log off;
        log_not_found off;
    }

    location / {
        return 301 https://$host$request_uri;
    }
}

server {
    listen 443 ssl;
    http2 on;
    server_name {{.Domain}}{{range .ServerAliases}} {{.}}{{end}};

    ssl_certificate /etc/letsencrypt/live/{{.SSLDomain}}/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/{{.SSLDomain}}/privkey.pem;

    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384;
    ssl_prefer_server_ciphers off;
    ssl_session_timeout 1d;
    ssl_session_cache shared:SSL:50m;
{{- if .EnableStapling}}
    ssl_stapling on;
    ssl_stapling_verify on;
{{- end}}

    add_header Strict-Transport-Security "max-age=63072000" always;

    resolver 127.0.0.11 valid=30s ipv6=off;
{{- range $.BlockedIPs}}
    deny {{.}};
{{- end}}
{{- range .Locations}}

    location {{.Path}} {
        set $upstream {{.Service}}:{{.ContainerPort}};
{{- if .StripPrefix}}
        rewrite ^{{.OriginalPath}}(.*)$ /$1 break;
{{- end}}
        proxy_pass {{.Protocol}}://$upstream;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_connect_timeout 60s;
        proxy_send_timeout {{.ProxyTimeout}}s;
        proxy_read_timeout {{.ProxyTimeout}}s;
{{- if $.SecurityEnabled}}
        log_by_lua_block {
            security.capture_event()
        }
{{- end}}
    }
{{- end}}
{{- range $.RateLimits}}

    location {{.Path}} {
        limit_req zone={{.Zone}} burst={{.Burst}} nodelay;
        limit_req_status 429;
        set $upstream {{$.DeploymentName}}:80;
        proxy_pass http://$upstream;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
{{- if $.SecurityEnabled}}
        log_by_lua_block {
            security.capture_event()
        }
{{- end}}
    }
{{- end}}
}
{{end}}`

const multiRouteMixedTemplate = `{{- range .Servers}}
{{- if .HasSSL}}
server {
    listen 80;
    server_name {{.Domain}}{{range .ServerAliases}} {{.}}{{end}};

    location /.well-known/acme-challenge/ {
        root {{$.ContainerWebrootPath}};
        access_log off;
        log_not_found off;
    }

    location / {
        return 301 https://$host$request_uri;
    }
}

server {
    listen 443 ssl;
    http2 on;
    server_name {{.Domain}}{{range .ServerAliases}} {{.}}{{end}};

    ssl_certificate /etc/letsencrypt/live/{{.SSLDomain}}/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/{{.SSLDomain}}/privkey.pem;

    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384;
    ssl_prefer_server_ciphers off;
    ssl_session_timeout 1d;
    ssl_session_cache shared:SSL:50m;
{{- if .EnableStapling}}
    ssl_stapling on;
    ssl_stapling_verify on;
{{- end}}

    add_header Strict-Transport-Security "max-age=63072000" always;

    resolver 127.0.0.11 valid=30s ipv6=off;
{{- range $.BlockedIPs}}
    deny {{.}};
{{- end}}
{{- range .Locations}}

    location {{.Path}} {
        set $upstream {{.Service}}:{{.ContainerPort}};
{{- if .StripPrefix}}
        rewrite ^{{.OriginalPath}}(.*)$ /$1 break;
{{- end}}
        proxy_pass {{.Protocol}}://$upstream;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_connect_timeout 60s;
        proxy_send_timeout {{.ProxyTimeout}}s;
        proxy_read_timeout {{.ProxyTimeout}}s;
{{- if $.SecurityEnabled}}
        log_by_lua_block {
            security.capture_event()
        }
{{- end}}
    }
{{- end}}
{{- range $.RateLimits}}

    location {{.Path}} {
        limit_req zone={{.Zone}} burst={{.Burst}} nodelay;
        limit_req_status 429;
        set $upstream {{$.DeploymentName}}:80;
        proxy_pass http://$upstream;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
{{- if $.SecurityEnabled}}
        log_by_lua_block {
            security.capture_event()
        }
{{- end}}
    }
{{- end}}
}
{{- else}}
server {
    listen 80;
    server_name {{.Domain}}{{range .ServerAliases}} {{.}}{{end}};

    resolver 127.0.0.11 valid=30s ipv6=off;
{{- range $.BlockedIPs}}
    deny {{.}};
{{- end}}
{{- range .Locations}}

    location {{.Path}} {
        set $upstream {{.Service}}:{{.ContainerPort}};
{{- if .StripPrefix}}
        rewrite ^{{.OriginalPath}}(.*)$ /$1 break;
{{- end}}
        proxy_pass {{.Protocol}}://$upstream;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_connect_timeout 60s;
        proxy_send_timeout {{.ProxyTimeout}}s;
        proxy_read_timeout {{.ProxyTimeout}}s;
{{- if $.SecurityEnabled}}
        log_by_lua_block {
            security.capture_event()
        }
{{- end}}
    }
{{- end}}
{{- range $.RateLimits}}

    location {{.Path}} {
        limit_req zone={{.Zone}} burst={{.Burst}} nodelay;
        limit_req_status 429;
        set $upstream {{$.DeploymentName}}:80;
        proxy_pass http://$upstream;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
{{- if $.SecurityEnabled}}
        log_by_lua_block {
            security.capture_event()
        }
{{- end}}
    }
{{- end}}

    location /.well-known/acme-challenge/ {
        root {{$.ContainerWebrootPath}};
        access_log off;
        log_not_found off;
    }
}
{{- end}}
{{end}}`

func sanitizeZoneName(path string) string {
	name := strings.ReplaceAll(path, "/", "_")
	name = strings.ReplaceAll(name, "*", "")
	name = strings.ReplaceAll(name, ".", "_")
	name = strings.Trim(name, "_")
	if name == "" {
		name = "default"
	}
	if len(name) > 20 {
		name = name[:20]
	}
	return name
}

// UpdateDeploymentRateLimits writes per-deployment rate limit zones to rate_limits.conf
func (m *Manager) UpdateDeploymentRateLimits(deploymentName string, rateLimits []models.DeploymentRateLimit) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.updateRateLimitsInternal(deploymentName, rateLimits)
}

func (m *Manager) updateRateLimitsInternal(deploymentName string, rateLimits []models.DeploymentRateLimit) error {
	rateLimitsPath := filepath.Join(m.configPath, "rate_limits.conf")

	// Read existing content
	existingContent, err := os.ReadFile(rateLimitsPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read rate_limits.conf: %w", err)
	}

	// Parse existing zones, keeping non-deployment zones
	var globalZones []string
	deploymentZones := make(map[string][]string)

	if len(existingContent) > 0 {
		lines := strings.Split(string(existingContent), "\n")
		currentDeployment := ""
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "# Deployment:") {
				currentDeployment = strings.TrimPrefix(line, "# Deployment:")
				currentDeployment = strings.TrimSpace(currentDeployment)
			} else if strings.HasPrefix(line, "limit_req_zone") {
				if currentDeployment != "" {
					if currentDeployment != deploymentName {
						deploymentZones[currentDeployment] = append(deploymentZones[currentDeployment], line)
					}
				} else {
					globalZones = append(globalZones, line)
				}
			} else if line == "" || strings.HasPrefix(line, "#") {
				if !strings.HasPrefix(line, "# Deployment:") && currentDeployment == "" {
					continue
				}
			}
		}
	}

	// Add new zones for this deployment
	if len(rateLimits) > 0 {
		var newZones []string
		for _, rl := range rateLimits {
			if !rl.Enabled {
				continue
			}
			zoneName := fmt.Sprintf("%s_%s", deploymentName, sanitizeZoneName(rl.Path))
			zone := fmt.Sprintf("limit_req_zone $binary_remote_addr zone=%s:10m rate=%dr/m;", zoneName, rl.Rate)
			newZones = append(newZones, zone)
		}
		if len(newZones) > 0 {
			deploymentZones[deploymentName] = newZones
		}
	}

	// Write updated content
	var buf bytes.Buffer
	buf.WriteString("# Auto-generated by FlatRun Security\n")
	buf.WriteString("# Do not edit manually - changes will be overwritten\n\n")

	if len(globalZones) > 0 {
		buf.WriteString("# Global rate limit zones\n")
		for _, zone := range globalZones {
			buf.WriteString(zone + "\n")
		}
		buf.WriteString("\n")
	}

	for depName, zones := range deploymentZones {
		buf.WriteString(fmt.Sprintf("# Deployment: %s\n", depName))
		for _, zone := range zones {
			buf.WriteString(zone + "\n")
		}
		buf.WriteString("\n")
	}

	if len(globalZones) == 0 && len(deploymentZones) == 0 {
		buf.WriteString("# No rate limit zones defined\n")
	}

	return os.WriteFile(rateLimitsPath, buf.Bytes(), 0644)
}

// RemoveDeploymentRateLimits removes rate limit zones for a deployment
func (m *Manager) RemoveDeploymentRateLimits(deploymentName string) error {
	return m.UpdateDeploymentRateLimits(deploymentName, nil)
}

// ValidateSecurityHooks checks that a vhost has the correct security hooks based on the expected state
func (m *Manager) ValidateSecurityHooks(deploymentName string, shouldHaveHooks bool) error {
	content, err := m.GetVirtualHost(deploymentName)
	if err != nil {
		return fmt.Errorf("failed to read vhost: %w", err)
	}

	hasHooks := strings.Contains(content, "security.capture_event()")
	hasLogByLua := strings.Contains(content, "log_by_lua_block")

	if shouldHaveHooks {
		if !hasHooks {
			return fmt.Errorf("security enabled but vhost missing security.capture_event() call")
		}
		if !hasLogByLua {
			return fmt.Errorf("security enabled but vhost missing log_by_lua_block")
		}

		// Check hooks are inside location blocks
		lines := strings.Split(content, "\n")
		inLocation := false
		foundHookInLocation := false

		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "location ") {
				inLocation = true
			}
			if inLocation && strings.Contains(trimmed, "security.capture_event()") {
				foundHookInLocation = true
			}
			if trimmed == "}" && inLocation {
				inLocation = false
			}
		}

		if !foundHookInLocation {
			return fmt.Errorf("security hook not properly placed inside location block")
		}
	} else {
		if hasHooks {
			return fmt.Errorf("security disabled but vhost still contains security.capture_event()")
		}
	}

	return nil
}

// SecurityHookStatus returns details about security hooks in a vhost
type SecurityHookStatus struct {
	HasHooks           bool     `json:"has_hooks"`
	HookLocations      []string `json:"hook_locations"`
	ProperlyConfigured bool     `json:"properly_configured"`
}

// GetSecurityHookStatus returns detailed info about security hooks in a vhost
func (m *Manager) GetSecurityHookStatus(deploymentName string) (*SecurityHookStatus, error) {
	content, err := m.GetVirtualHost(deploymentName)
	if err != nil {
		return nil, err
	}

	status := &SecurityHookStatus{
		HasHooks:      strings.Contains(content, "security.capture_event()"),
		HookLocations: []string{},
	}

	lines := strings.Split(content, "\n")
	currentLocation := ""
	inLocation := false
	depth := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "location ") {
			inLocation = true
			depth = 1
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				currentLocation = parts[1]
			}
		}

		if inLocation {
			depth += strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
			if strings.Contains(trimmed, "security.capture_event()") {
				status.HookLocations = append(status.HookLocations, currentLocation)
			}
			if depth <= 0 {
				inLocation = false
				currentLocation = ""
			}
		}
	}

	// Properly configured if hooks are present and all found in location blocks
	status.ProperlyConfigured = status.HasHooks && len(status.HookLocations) > 0

	return status, nil
}
