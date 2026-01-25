package dns

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
)

type PowerDNSManager struct {
	config *config.Config
	mu     sync.RWMutex
	client *http.Client
}

func NewPowerDNSManager(cfg *config.Config) *PowerDNSManager {
	return &PowerDNSManager{
		config: cfg,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (m *PowerDNSManager) UpdateConfig(cfg *config.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config = cfg
}

type PowerDNSStatus struct {
	Running bool   `json:"running"`
	Version string `json:"version,omitempty"`
}

func (m *PowerDNSManager) GetStatus() (*PowerDNSStatus, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.config.Infrastructure.PowerDNS.Enabled {
		return &PowerDNSStatus{Running: false}, nil
	}

	containerName := m.config.Infrastructure.PowerDNS.Container
	cmd := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", containerName)
	output, err := cmd.Output()
	if err != nil {
		return &PowerDNSStatus{Running: false}, nil
	}

	running := strings.TrimSpace(string(output)) == "true"
	status := &PowerDNSStatus{Running: running}

	if running {
		if version, err := m.getAPIVersion(); err == nil {
			status.Version = version
		}
	}

	return status, nil
}

func (m *PowerDNSManager) getAPIVersion() (string, error) {
	resp, err := m.apiRequest("GET", "/api/v1/servers/localhost", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var server struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&server); err != nil {
		return "", err
	}
	return server.Version, nil
}

func (m *PowerDNSManager) EnableService() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	pdnsDir := m.getPowerDNSDir()
	if err := os.MkdirAll(pdnsDir, 0755); err != nil {
		return fmt.Errorf("failed to create PowerDNS directory: %w", err)
	}

	dataDir := filepath.Join(pdnsDir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	if err := m.initializeDatabase(dataDir); err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}

	if err := m.writeComposeFile(); err != nil {
		return fmt.Errorf("failed to write docker-compose: %w", err)
	}

	cmd := exec.Command("docker", "compose", "-f", filepath.Join(pdnsDir, "docker-compose.yml"), "up", "-d")
	cmd.Dir = pdnsDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to start PowerDNS: %s - %w", string(output), err)
	}

	m.config.Infrastructure.PowerDNS.Enabled = true
	return nil
}

func (m *PowerDNSManager) initializeDatabase(dataDir string) error {
	dbPath := filepath.Join(dataDir, "pdns.sqlite3")

	if _, err := os.Stat(dbPath); err == nil {
		return nil
	}

	schema := `
PRAGMA foreign_keys = 1;
CREATE TABLE domains (
  id                    INTEGER PRIMARY KEY,
  name                  VARCHAR(255) NOT NULL COLLATE NOCASE,
  master                VARCHAR(128) DEFAULT NULL,
  last_check            INTEGER DEFAULT NULL,
  type                  VARCHAR(8) NOT NULL,
  notified_serial       INTEGER DEFAULT NULL,
  account               VARCHAR(40) DEFAULT NULL,
  options               VARCHAR(65535) DEFAULT NULL,
  catalog               VARCHAR(255) DEFAULT NULL
);
CREATE UNIQUE INDEX name_index ON domains(name);
CREATE INDEX catalog_idx ON domains(catalog);
CREATE TABLE records (
  id                    INTEGER PRIMARY KEY,
  domain_id             INTEGER DEFAULT NULL,
  name                  VARCHAR(255) DEFAULT NULL,
  type                  VARCHAR(10) DEFAULT NULL,
  content               VARCHAR(65535) DEFAULT NULL,
  ttl                   INTEGER DEFAULT NULL,
  prio                  INTEGER DEFAULT NULL,
  disabled              BOOLEAN DEFAULT 0,
  ordername             VARCHAR(255),
  auth                  BOOL DEFAULT 1,
  FOREIGN KEY(domain_id) REFERENCES domains(id) ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE INDEX records_lookup_idx ON records(name, type);
CREATE INDEX records_lookup_id_idx ON records(domain_id, name, type);
CREATE INDEX records_order_idx ON records(domain_id, ordername);
CREATE TABLE supermasters (
  ip                    VARCHAR(64) NOT NULL,
  nameserver            VARCHAR(255) NOT NULL COLLATE NOCASE,
  account               VARCHAR(40) NOT NULL
);
CREATE UNIQUE INDEX ip_nameserver_pk ON supermasters(ip, nameserver);
CREATE TABLE comments (
  id                    INTEGER PRIMARY KEY,
  domain_id             INTEGER NOT NULL,
  name                  VARCHAR(255) NOT NULL,
  type                  VARCHAR(10) NOT NULL,
  modified_at           INT NOT NULL,
  account               VARCHAR(40) DEFAULT NULL,
  comment               VARCHAR(65535) NOT NULL,
  FOREIGN KEY(domain_id) REFERENCES domains(id) ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE INDEX comments_idx ON comments(domain_id, name, type);
CREATE INDEX comments_order_idx ON comments(domain_id, modified_at);
CREATE TABLE domainmetadata (
  id                    INTEGER PRIMARY KEY,
  domain_id             INT NOT NULL,
  kind                  VARCHAR(32) COLLATE NOCASE,
  content               TEXT,
  FOREIGN KEY(domain_id) REFERENCES domains(id) ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE INDEX domainmetaidindex ON domainmetadata(domain_id);
CREATE TABLE cryptokeys (
  id                    INTEGER PRIMARY KEY,
  domain_id             INT NOT NULL,
  flags                 INT NOT NULL,
  active                BOOL,
  published             BOOL DEFAULT 1,
  content               TEXT,
  FOREIGN KEY(domain_id) REFERENCES domains(id) ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE INDEX domainidindex ON cryptokeys(domain_id);
CREATE TABLE tsigkeys (
  id                    INTEGER PRIMARY KEY,
  name                  VARCHAR(255) COLLATE NOCASE,
  algorithm             VARCHAR(50) COLLATE NOCASE,
  secret                VARCHAR(255)
);
CREATE UNIQUE INDEX namealgoindex ON tsigkeys(name, algorithm);
`

	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	uid := fmt.Sprintf("%d", os.Getuid())
	gid := fmt.Sprintf("%d", os.Getgid())

	cmd := exec.Command("docker", "run", "--rm",
		"-v", absDataDir+":/data",
		"alpine:latest",
		"sh", "-c", "apk add --no-cache sqlite > /dev/null 2>&1 && sqlite3 /data/pdns.sqlite3 \""+schema+"\" && chown "+uid+":"+gid+" /data/pdns.sqlite3")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("database init failed: %s - %w", string(output), err)
	}

	return nil
}

func (m *PowerDNSManager) DisableService() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	pdnsDir := m.getPowerDNSDir()
	composePath := filepath.Join(pdnsDir, "docker-compose.yml")

	if _, err := os.Stat(composePath); err == nil {
		cmd := exec.Command("docker", "compose", "-f", composePath, "down")
		cmd.Dir = pdnsDir
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to stop PowerDNS: %s - %w", string(output), err)
		}
	}

	m.config.Infrastructure.PowerDNS.Enabled = false
	return nil
}

func (m *PowerDNSManager) RestartService() error {
	m.mu.RLock()
	containerName := m.config.Infrastructure.PowerDNS.Container
	m.mu.RUnlock()

	cmd := exec.Command("docker", "restart", containerName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to restart PowerDNS: %s - %w", string(output), err)
	}
	return nil
}

func (m *PowerDNSManager) GetInfraService() models.InfraService {
	m.mu.RLock()
	defer m.mu.RUnlock()

	svc := models.InfraService{
		Name:    models.InfraTypePowerDNS,
		Type:    models.InfraTypePowerDNS,
		Image:   m.config.Infrastructure.PowerDNS.Image,
		Managed: m.config.Infrastructure.PowerDNS.Enabled,
		Config: map[string]any{
			"container": m.config.Infrastructure.PowerDNS.Container,
			"api_port":  m.config.Infrastructure.PowerDNS.APIPort,
			"dns_port":  m.config.Infrastructure.PowerDNS.DNSPort,
		},
	}

	if !m.config.Infrastructure.PowerDNS.Enabled {
		svc.Status = models.InfraStatusStopped
		return svc
	}

	containerName := m.config.Infrastructure.PowerDNS.Container
	svc.Status, svc.ContainerID, svc.Health, svc.CreatedAt = m.getContainerStatus(containerName)

	return svc
}

func (m *PowerDNSManager) getContainerStatus(containerName string) (status, containerID, health string, createdAt time.Time) {
	if containerName == "" {
		return models.InfraStatusUnknown, "", "", time.Time{}
	}

	cmd := exec.Command("docker", "inspect", "--format", "{{json .}}", containerName)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return models.InfraStatusStopped, "", "", time.Time{}
	}

	var container struct {
		ID    string `json:"Id"`
		State struct {
			Status  string `json:"Status"`
			Running bool   `json:"Running"`
			Health  *struct {
				Status string `json:"Status"`
			} `json:"Health,omitempty"`
		} `json:"State"`
		Created string `json:"Created"`
	}

	if err := json.Unmarshal(stdout.Bytes(), &container); err != nil {
		return models.InfraStatusUnknown, "", "", time.Time{}
	}

	containerID = container.ID[:12]

	if container.State.Running {
		status = models.InfraStatusRunning
	} else {
		status = models.InfraStatusStopped
	}

	if container.State.Health != nil {
		health = container.State.Health.Status
	}

	if created, err := time.Parse(time.RFC3339Nano, container.Created); err == nil {
		createdAt = created
	}

	return status, containerID, health, createdAt
}

func (m *PowerDNSManager) getPowerDNSDir() string {
	return filepath.Join(m.config.DeploymentsPath, "powerdns")
}

func (m *PowerDNSManager) writeComposeFile() error {
	pdnsDir := m.getPowerDNSDir()
	composePath := filepath.Join(pdnsDir, "docker-compose.yml")

	cfg := m.config.Infrastructure.PowerDNS

	pdnsConf := fmt.Sprintf(`launch=gsqlite3
gsqlite3-database=/var/lib/powerdns/pdns.sqlite3
gsqlite3-dnssec=yes
local-address=0.0.0.0,::
api=yes
api-key=%s
webserver=yes
webserver-address=0.0.0.0
webserver-port=8081
webserver-allow-from=0.0.0.0/0,::/0
default-soa-content=ns1.@ hostmaster.@ 0 10800 3600 604800 3600
`, cfg.APIKey)

	confPath := filepath.Join(pdnsDir, "pdns.conf")
	if err := os.WriteFile(confPath, []byte(pdnsConf), 0644); err != nil {
		return fmt.Errorf("failed to write pdns.conf: %w", err)
	}

	content := fmt.Sprintf(`services:
  pdns:
    image: %s
    container_name: %s
    restart: unless-stopped
    ports:
      - "%d:53/udp"
      - "%d:53/tcp"
      - "127.0.0.1:%d:8081"
    volumes:
      - ./data:/var/lib/powerdns
      - ./pdns.conf:/etc/powerdns/pdns.conf:ro
    networks:
      - proxy

networks:
  proxy:
    external: true
`, cfg.Image, cfg.Container, cfg.DNSPort, cfg.DNSPort, cfg.APIPort)

	return os.WriteFile(composePath, []byte(content), 0644)
}

func (m *PowerDNSManager) getAPIKey() string {
	confPath := filepath.Join(m.getPowerDNSDir(), "pdns.conf")
	data, err := os.ReadFile(confPath)
	if err != nil {
		return m.config.Infrastructure.PowerDNS.APIKey
	}

	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "api-key=") {
			return strings.TrimPrefix(line, "api-key=")
		}
	}

	return m.config.Infrastructure.PowerDNS.APIKey
}

func (m *PowerDNSManager) apiRequest(method, path string, body interface{}) (*http.Response, error) {
	cfg := m.config.Infrastructure.PowerDNS
	url := fmt.Sprintf("http://127.0.0.1:%d%s", cfg.APIPort, path)

	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-API-Key", m.getAPIKey())
	req.Header.Set("Content-Type", "application/json")

	return m.client.Do(req)
}

type Zone struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Serial      int      `json:"serial"`
	DNSSec      bool     `json:"dnssec"`
	RRSets      []RRSet  `json:"rrsets,omitempty"`
	Nameservers []string `json:"nameservers,omitempty"`
}

type RRSet struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	TTL        int      `json:"ttl"`
	ChangeType string   `json:"changetype,omitempty"`
	Records    []Record `json:"records"`
}

type Record struct {
	Content  string `json:"content"`
	Disabled bool   `json:"disabled"`
}

type ZoneCreate struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Nameservers []string `json:"nameservers,omitempty"`
}

func (m *PowerDNSManager) ListZones() ([]Zone, error) {
	resp, err := m.apiRequest("GET", "/api/v1/servers/localhost/zones", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error: %s", string(body))
	}

	var zones []Zone
	if err := json.NewDecoder(resp.Body).Decode(&zones); err != nil {
		return nil, err
	}
	return zones, nil
}

func (m *PowerDNSManager) GetZone(zoneID string) (*Zone, error) {
	resp, err := m.apiRequest("GET", fmt.Sprintf("/api/v1/servers/localhost/zones/%s", zoneID), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error: %s", string(body))
	}

	var zone Zone
	if err := json.NewDecoder(resp.Body).Decode(&zone); err != nil {
		return nil, err
	}
	return &zone, nil
}

func (m *PowerDNSManager) CreateZone(create ZoneCreate) (*Zone, error) {
	if !strings.HasSuffix(create.Name, ".") {
		create.Name = create.Name + "."
	}

	payload := map[string]interface{}{
		"name": create.Name,
		"kind": create.Kind,
	}
	if len(create.Nameservers) > 0 {
		payload["nameservers"] = create.Nameservers
	}

	resp, err := m.apiRequest("POST", "/api/v1/servers/localhost/zones", payload)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error: %s", string(body))
	}

	var zone Zone
	if err := json.NewDecoder(resp.Body).Decode(&zone); err != nil {
		return nil, err
	}
	return &zone, nil
}

func (m *PowerDNSManager) DeleteZone(zoneID string) error {
	resp, err := m.apiRequest("DELETE", fmt.Sprintf("/api/v1/servers/localhost/zones/%s", zoneID), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error: %s", string(body))
	}
	return nil
}

func (m *PowerDNSManager) UpdateRecords(zoneID string, rrsets []RRSet) error {
	payload := map[string]interface{}{
		"rrsets": rrsets,
	}

	resp, err := m.apiRequest("PATCH", fmt.Sprintf("/api/v1/servers/localhost/zones/%s", zoneID), payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error: %s", string(body))
	}
	return nil
}
