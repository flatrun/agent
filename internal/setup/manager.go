package setup

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/flatrun/agent/internal/system"
	"github.com/flatrun/agent/pkg/config"
)

type State struct {
	Initialized bool   `json:"initialized"`
	CompletedAt string `json:"completed_at,omitempty"`
}

type Manager struct {
	config     *config.Config
	configPath string
	cachedIP   string
}

func NewManager(cfg *config.Config, configPath string) *Manager {
	return &Manager{
		config:     cfg,
		configPath: configPath,
	}
}

func (m *Manager) Config() *config.Config {
	return m.config
}

func (m *Manager) ConfigPath() string {
	return m.configPath
}

func (m *Manager) statePath() string {
	return filepath.Join(m.config.DeploymentsPath, ".flatrun", "setup.json")
}

func IsComplete(deploymentsPath string) bool {
	data, err := os.ReadFile(filepath.Join(deploymentsPath, ".flatrun", "setup.json"))
	if err != nil {
		return false
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return false
	}
	return state.Initialized
}

func (m *Manager) IsComplete() bool {
	return IsComplete(m.config.DeploymentsPath)
}

func (m *Manager) MarkComplete() error {
	state := State{
		Initialized: true,
		CompletedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(m.statePath())
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(m.statePath(), data, 0644)
}

func (m *Manager) GetInstanceIP() string {
	if m.cachedIP != "" {
		return m.cachedIP
	}
	ip := ResolvePublicIP()
	m.cachedIP = ip
	return ip
}

func ResolvePublicIP() string {
	if ip, err := system.GetPublicIP("4"); err == nil {
		return ip
	}

	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}

	return "127.0.0.1"
}
