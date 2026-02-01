package setup

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/flatrun/agent/pkg/config"
)

type DeploymentMode string

const (
	ModeFull      DeploymentMode = "full"
	ModeAgentOnly DeploymentMode = "agent-only"
)

type Manager struct {
	mu         sync.RWMutex
	db         *DB
	config     *config.Config
	configPath string
}

type SetupStatus struct {
	Initialized    bool   `json:"initialized"`
	InstanceIP     string `json:"instance_ip"`
	AgentVersion   string `json:"agent_version"`
	DeploymentMode string `json:"deployment_mode,omitempty"`
	UIOrigin       string `json:"ui_origin,omitempty"`
	Domain         string `json:"domain,omitempty"`
	CloudProvider  string `json:"cloud_provider,omitempty"`
}

type InitResponse struct {
	JWTSecret string `json:"jwt_secret"`
	Mode      string `json:"mode"`
}

type DNSCheckResult struct {
	Domain   string   `json:"domain"`
	Expected string   `json:"expected"`
	Actual   []string `json:"actual"`
	Valid    bool     `json:"valid"`
	Message  string   `json:"message"`
}

type UserResponse struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	APIKey   string `json:"api_key"`
}

var Version = "dev"

func NewManager(deploymentsPath string, cfg *config.Config, configPath string) (*Manager, error) {
	db, err := NewSetupDB(deploymentsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize setup database: %w", err)
	}

	m := &Manager{
		db:         db,
		config:     cfg,
		configPath: configPath,
	}

	if err := m.detectEnvironment(); err != nil {
		log.Printf("Warning: failed to detect environment: %v", err)
	}

	return m, nil
}

func (m *Manager) Close() error {
	return m.db.Close()
}

func (m *Manager) IsInitialized() bool {
	state, err := m.db.GetState()
	if err != nil {
		log.Printf("Warning: failed to get setup state: %v", err)
		return false
	}
	return state.Initialized
}

func (m *Manager) GetStatus() (*SetupStatus, error) {
	state, err := m.db.GetState()
	if err != nil {
		return nil, err
	}

	return &SetupStatus{
		Initialized:    state.Initialized,
		InstanceIP:     state.InstanceIP,
		AgentVersion:   Version,
		DeploymentMode: state.DeploymentMode,
		UIOrigin:       state.UIOrigin,
		Domain:         state.Domain,
		CloudProvider:  state.CloudProvider,
	}, nil
}

func (m *Manager) Initialize(mode DeploymentMode) (*InitResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, err := m.db.GetState()
	if err != nil {
		return nil, err
	}

	if state.Initialized {
		return nil, fmt.Errorf("setup already completed")
	}

	jwtSecret := generateSecret(32)
	if err := m.db.SetJWTSecret(jwtSecret); err != nil {
		return nil, fmt.Errorf("failed to save JWT secret: %w", err)
	}

	if err := m.db.SetDeploymentMode(string(mode)); err != nil {
		return nil, fmt.Errorf("failed to save deployment mode: %w", err)
	}

	m.config.Auth.JWTSecret = jwtSecret
	if err := config.Save(m.config, m.configPath); err != nil {
		log.Printf("Warning: failed to save config: %v", err)
	}

	return &InitResponse{
		JWTSecret: jwtSecret,
		Mode:      string(mode),
	}, nil
}

func (m *Manager) ConfigureDomain(domain string, autoSSL bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.db.SetDomain(domain, autoSSL); err != nil {
		return fmt.Errorf("failed to save domain: %w", err)
	}

	m.config.Domain.DefaultDomain = domain
	m.config.Domain.AutoSSL = autoSSL
	if err := config.Save(m.config, m.configPath); err != nil {
		log.Printf("Warning: failed to save config: %v", err)
	}

	return nil
}

func (m *Manager) ConfigureCORS(uiOrigin string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.db.SetUIOrigin(uiOrigin); err != nil {
		return fmt.Errorf("failed to save UI origin: %w", err)
	}

	m.config.API.EnableCORS = true
	found := false
	for _, origin := range m.config.API.AllowedOrigins {
		if origin == uiOrigin {
			found = true
			break
		}
	}
	if !found {
		m.config.API.AllowedOrigins = append(m.config.API.AllowedOrigins, uiOrigin)
	}

	if err := config.Save(m.config, m.configPath); err != nil {
		log.Printf("Warning: failed to save config: %v", err)
	}

	return nil
}

func (m *Manager) VerifyDNS(domain string) (*DNSCheckResult, error) {
	state, err := m.db.GetState()
	if err != nil {
		return nil, err
	}

	expectedIP := state.InstanceIP
	if expectedIP == "" {
		return nil, fmt.Errorf("instance IP not detected")
	}

	ips, err := net.LookupIP(domain)
	if err != nil {
		return &DNSCheckResult{
			Domain:   domain,
			Expected: expectedIP,
			Actual:   []string{},
			Valid:    false,
			Message:  fmt.Sprintf("DNS lookup failed: %v", err),
		}, nil
	}

	var resolvedIPs []string
	valid := false
	for _, ip := range ips {
		ipStr := ip.String()
		resolvedIPs = append(resolvedIPs, ipStr)
		if ipStr == expectedIP {
			valid = true
		}
	}

	result := &DNSCheckResult{
		Domain:   domain,
		Expected: expectedIP,
		Actual:   resolvedIPs,
		Valid:    valid,
	}

	if valid {
		result.Message = "DNS verification successful"
	} else {
		result.Message = fmt.Sprintf("Domain does not point to instance IP. Expected %s, got %v", expectedIP, resolvedIPs)
	}

	return result, nil
}

func (m *Manager) Complete() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.db.MarkInitialized(); err != nil {
		return fmt.Errorf("failed to mark setup as complete: %w", err)
	}

	return nil
}

func (m *Manager) GetAllowedOrigins() []string {
	state, err := m.db.GetState()
	if err != nil {
		return m.config.API.AllowedOrigins
	}

	origins := make([]string, len(m.config.API.AllowedOrigins))
	copy(origins, m.config.API.AllowedOrigins)

	if state.UIOrigin != "" {
		found := false
		for _, o := range origins {
			if o == state.UIOrigin {
				found = true
				break
			}
		}
		if !found {
			origins = append(origins, state.UIOrigin)
		}
	}

	return origins
}

func (m *Manager) detectEnvironment() error {
	ip, provider := m.detectCloudEnvironment()

	if ip != "" {
		if err := m.db.SetInstanceIP(ip); err != nil {
			return err
		}
	}

	if provider != "" {
		if err := m.db.SetCloudProvider(provider); err != nil {
			return err
		}
	}

	return nil
}

func (m *Manager) detectCloudEnvironment() (string, string) {
	type cloudMetadata struct {
		name   string
		ipURL  string
		idURL  string
		header map[string]string
	}

	clouds := []cloudMetadata{
		{
			name:   "digitalocean",
			ipURL:  "http://169.254.169.254/metadata/v1/interfaces/public/0/ipv4/address",
			idURL:  "http://169.254.169.254/metadata/v1/id",
			header: nil,
		},
		{
			name:   "aws",
			ipURL:  "http://169.254.169.254/latest/meta-data/public-ipv4",
			idURL:  "http://169.254.169.254/latest/meta-data/instance-id",
			header: nil,
		},
		{
			name:   "gcp",
			ipURL:  "http://metadata.google.internal/computeMetadata/v1/instance/network-interfaces/0/access-configs/0/external-ip",
			idURL:  "http://metadata.google.internal/computeMetadata/v1/instance/id",
			header: map[string]string{"Metadata-Flavor": "Google"},
		},
		{
			name:   "azure",
			ipURL:  "http://169.254.169.254/metadata/instance/network/interface/0/ipv4/ipAddress/0/publicIpAddress?api-version=2021-02-01&format=text",
			idURL:  "http://169.254.169.254/metadata/instance/compute/vmId?api-version=2021-02-01&format=text",
			header: map[string]string{"Metadata": "true"},
		},
	}

	client := &http.Client{Timeout: 2 * time.Second}

	for _, cloud := range clouds {
		req, err := http.NewRequest("GET", cloud.idURL, nil)
		if err != nil {
			continue
		}
		for k, v := range cloud.header {
			req.Header.Set(k, v)
		}

		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != 200 {
			if resp != nil {
				resp.Body.Close()
			}
			continue
		}
		resp.Body.Close()

		req, err = http.NewRequest("GET", cloud.ipURL, nil)
		if err != nil {
			continue
		}
		for k, v := range cloud.header {
			req.Header.Set(k, v)
		}

		resp, err = client.Do(req)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == 200 {
			body, err := io.ReadAll(resp.Body)
			if err == nil {
				ip := strings.TrimSpace(string(body))
				if ip != "" && net.ParseIP(ip) != nil {
					return ip, cloud.name
				}
			}
		}
	}

	ip := m.detectPublicIPFallback()
	return ip, ""
}

func (m *Manager) detectPublicIPFallback() string {
	services := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://icanhazip.com",
	}

	client := &http.Client{Timeout: 5 * time.Second}

	for _, url := range services {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == 200 {
			body, err := io.ReadAll(resp.Body)
			if err == nil {
				ip := strings.TrimSpace(string(body))
				if net.ParseIP(ip) != nil {
					return ip
				}
			}
		}
	}

	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err == nil {
		defer conn.Close()
		localAddr := conn.LocalAddr().(*net.UDPAddr)
		return localAddr.IP.String()
	}

	return ""
}

func (m *Manager) SetInstanceIP(ip string) error {
	return m.db.SetInstanceIP(ip)
}

func (m *Manager) InstallUI() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	script := "/opt/flatrun/bin/install-ui.sh"
	if _, err := os.Stat(script); os.IsNotExist(err) {
		return fmt.Errorf("UI installer script not found at %s", script)
	}

	cmd := exec.Command("/bin/bash", script)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("UI installation failed: %v\nOutput: %s", err, string(output))
	}

	return nil
}

func generateSecret(length int) string {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		fallback := make([]byte, length)
		for i := range fallback {
			fallback[i] = byte(os.Getpid()>>i) ^ byte(time.Now().UnixNano()>>i)
		}
		return hex.EncodeToString(fallback)
	}
	return hex.EncodeToString(bytes)
}
