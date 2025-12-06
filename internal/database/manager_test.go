package database

import (
	"os/exec"
	"strings"
	"testing"
)

func TestParseContainerIP(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected string
	}{
		{
			name:     "single network",
			json:     `{"bridge":{"IPAddress":"172.17.0.2"}}`,
			expected: "172.17.0.2",
		},
		{
			name:     "multiple networks prefers database",
			json:     `{"bridge":{"IPAddress":"172.17.0.2"},"database":{"IPAddress":"172.20.0.5"}}`,
			expected: "172.20.0.5",
		},
		{
			name:     "multiple networks without database",
			json:     `{"proxy":{"IPAddress":"172.18.0.3"},"bridge":{"IPAddress":"172.17.0.2"}}`,
			expected: "172.18.0.3", // returns first found
		},
		{
			name:     "empty networks",
			json:     `{}`,
			expected: "",
		},
		{
			name:     "invalid json",
			json:     `not json`,
			expected: "",
		},
		{
			name:     "network with empty IP",
			json:     `{"bridge":{"IPAddress":""}}`,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseContainerIP([]byte(tt.json))
			// For "multiple networks without database", any valid IP is acceptable
			// since Go map iteration order is non-deterministic
			if tt.name == "multiple networks without database" {
				if result != "172.18.0.3" && result != "172.17.0.2" {
					t.Errorf("parseContainerIP() = %q, want one of [172.18.0.3, 172.17.0.2]", result)
				}
			} else if result != tt.expected {
				t.Errorf("parseContainerIP() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestDockerInspectTemplate(t *testing.T) {
	// Skip if docker is not available
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	// Get a running container to test with
	cmd := exec.Command("docker", "ps", "-q", "--limit", "1")
	output, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(output)) == "" {
		t.Skip("no running containers available for testing")
	}

	containerID := strings.TrimSpace(string(output))

	// Test the template we use in resolveContainerConnection
	inspectCmd := exec.Command("docker", "inspect", "--format",
		"{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", containerID)
	ipOutput, err := inspectCmd.Output()

	if err != nil {
		t.Errorf("docker inspect template failed: %v", err)
	}

	ip := strings.TrimSpace(string(ipOutput))
	if ip == "" {
		t.Error("docker inspect returned empty IP")
	}

	// Verify it looks like an IP address
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		t.Errorf("expected IP address format, got: %s", ip)
	}
}

func TestResolveContainerConnection_Localhost(t *testing.T) {
	m := NewManager()

	tests := []struct {
		name         string
		cfg          *ConnectionConfig
		expectedHost string
	}{
		{
			name:         "localhost returns as-is",
			cfg:          &ConnectionConfig{Host: "localhost", Port: 3306},
			expectedHost: "localhost",
		},
		{
			name:         "127.0.0.1 returns as-is",
			cfg:          &ConnectionConfig{Host: "127.0.0.1", Port: 3306},
			expectedHost: "127.0.0.1",
		},
		{
			name:         "private IP 192.168.x.x returns as-is",
			cfg:          &ConnectionConfig{Host: "192.168.1.100", Port: 3306},
			expectedHost: "192.168.1.100",
		},
		{
			name:         "private IP 10.x.x.x returns as-is",
			cfg:          &ConnectionConfig{Host: "10.0.0.50", Port: 3306},
			expectedHost: "10.0.0.50",
		},
		{
			name:         "private IP 172.x.x.x returns as-is",
			cfg:          &ConnectionConfig{Host: "172.17.0.2", Port: 3306},
			expectedHost: "172.17.0.2",
		},
		{
			name:         "empty host returns localhost",
			cfg:          &ConnectionConfig{Host: "", Port: 3306},
			expectedHost: "localhost",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, _ := m.resolveContainerConnection(tt.cfg)

			if host != tt.expectedHost {
				t.Errorf("host = %q, want %q", host, tt.expectedHost)
			}

			if port != tt.cfg.Port {
				t.Errorf("port = %d, want %d", port, tt.cfg.Port)
			}
		})
	}
}

func TestResolveContainerConnection_ContainerOverridesHost(t *testing.T) {
	m := NewManager()

	cfg := &ConnectionConfig{
		Host:      "some-hostname",
		Container: "172.17.0.5",
		Port:      3306,
	}

	host, _, _ := m.resolveContainerConnection(cfg)

	// Container field should be used when it's an IP
	if host != "172.17.0.5" {
		t.Errorf("expected Container IP to be used, got: %s", host)
	}
}

func TestResolveContainerConnection_DockerContainer(t *testing.T) {
	// Skip if docker is not available
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	// Get a running container name to test with
	cmd := exec.Command("docker", "ps", "--format", "{{.Names}}", "--limit", "1")
	output, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(output)) == "" {
		t.Skip("no running containers available for testing")
	}

	containerName := strings.TrimSpace(string(output))
	m := NewManager()

	cfg := &ConnectionConfig{
		Host: containerName,
		Port: 3306,
		Type: "mysql",
	}

	host, _, err := m.resolveContainerConnection(cfg)
	if err != nil {
		t.Errorf("resolveContainerConnection failed: %v", err)
	}

	// Should return an IP address, not the container name
	if host == containerName {
		t.Errorf("expected IP address, got container name: %s", host)
	}

	// Verify it looks like an IP
	parts := strings.Split(host, ".")
	if len(parts) != 4 {
		t.Errorf("expected IP address format, got: %s", host)
	}
}

func TestResolveContainerConnection_NonExistentContainer(t *testing.T) {
	// Skip if docker is not available
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	m := NewManager()

	cfg := &ConnectionConfig{
		Host: "non-existent-container-xyz-123",
		Port: 3306,
		Type: "mysql",
	}

	host, _, _ := m.resolveContainerConnection(cfg)

	// Should fall back to the Host value when container doesn't exist
	if host != cfg.Host {
		t.Errorf("expected fallback to Host %q, got: %s", cfg.Host, host)
	}
}

func TestBuildDSN(t *testing.T) {
	m := NewManager()

	tests := []struct {
		name        string
		cfg         *ConnectionConfig
		expectError bool
		contains    string
	}{
		{
			name: "mysql DSN",
			cfg: &ConnectionConfig{
				Type:     "mysql",
				Host:     "172.17.0.2",
				Port:     3306,
				Username: "root",
				Password: "secret",
				Database: "testdb",
			},
			expectError: false,
			contains:    "root:secret@tcp(172.17.0.2:3306)/testdb",
		},
		{
			name: "postgresql DSN",
			cfg: &ConnectionConfig{
				Type:     "postgresql",
				Host:     "172.17.0.3",
				Port:     5432,
				Username: "postgres",
				Password: "secret",
				Database: "testdb",
			},
			expectError: false,
			contains:    "host=172.17.0.3",
		},
		{
			name: "unsupported type",
			cfg: &ConnectionConfig{
				Type: "mongodb",
				Host: "localhost",
				Port: 27017,
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dsn, err := m.buildDSN(tt.cfg)

			if tt.expectError && err == nil {
				t.Error("expected error, got nil")
			}

			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if !tt.expectError && !strings.Contains(dsn, tt.contains) {
				t.Errorf("DSN %q should contain %q", dsn, tt.contains)
			}
		})
	}
}
