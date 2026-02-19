package api

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/config"
	"gopkg.in/yaml.v3"
)

// dockerAvailable checks if docker is in PATH for CLI validation tests.
// docker compose config validates files without needing the daemon.
func dockerAvailable(t *testing.T) bool {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping docker CLI validation test in short mode")
		return false
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not in PATH, skipping CLI validation test")
		return false
	}
	return true
}

func TestExtractNetworkNames(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected []string
	}{
		{
			name:     "nil input",
			input:    nil,
			expected: nil,
		},
		{
			name:     "string slice",
			input:    []string{"proxy", "database"},
			expected: []string{"proxy", "database"},
		},
		{
			name:     "interface slice",
			input:    []interface{}{"proxy", "database"},
			expected: []string{"proxy", "database"},
		},
		{
			name:     "map format",
			input:    map[string]interface{}{"proxy": nil, "database": map[string]interface{}{"aliases": []string{"db"}}},
			expected: []string{"proxy", "database"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractNetworkNames(tt.input)
			if tt.expected == nil {
				if result != nil {
					t.Errorf("expected nil, got %v", result)
				}
				return
			}
			if len(result) != len(tt.expected) {
				t.Errorf("expected %d networks, got %d", len(tt.expected), len(result))
				return
			}
			for _, expected := range tt.expected {
				found := false
				for _, r := range result {
					if r == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected network %s not found in result %v", expected, result)
				}
			}
		})
	}
}

func TestComposeServiceUnmarshal(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{
			name: "simple compose with string networks",
			yaml: `
name: test
services:
  app:
    image: nginx:latest
    networks:
      - proxy
`,
			wantErr: false,
		},
		{
			name: "compose with map networks",
			yaml: `
name: test
services:
  app:
    image: nginx:latest
    networks:
      proxy:
      database:
        aliases:
          - db
`,
			wantErr: false,
		},
		{
			name: "compose with multiline environment",
			yaml: `
name: test
services:
  wordpress:
    image: wordpress:latest
    environment:
      WORDPRESS_DB_HOST: db
      WORDPRESS_CONFIG_EXTRA: |
        if (isset($_SERVER['HTTP_X_FORWARDED_PROTO']) && $_SERVER['HTTP_X_FORWARDED_PROTO'] === 'https') {
            $_SERVER['HTTPS'] = 'on';
        }
    networks:
      - proxy
`,
			wantErr: false,
		},
		{
			name: "compose with complex volumes",
			yaml: `
name: test
services:
  app:
    image: nginx:latest
    volumes:
      - type: bind
        source: ./data
        target: /data
      - wordpress_data:/var/www/html
    networks:
      - proxy
`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var compose composeFile
			err := yaml.Unmarshal([]byte(tt.yaml), &compose)
			if (err != nil) != tt.wantErr {
				t.Errorf("Unmarshal() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && len(compose.Services) == 0 {
				t.Error("expected at least one service")
			}
		})
	}
}

func TestValidateComposeWithCLI_ValidCompose(t *testing.T) {
	if !dockerAvailable(t) {
		return
	}
	validCompose := `name: test
services:
  app:
    image: nginx:alpine
`
	err := validateComposeWithCLI(validCompose)
	if err != nil {
		t.Errorf("validateComposeWithCLI(valid compose) = %v, want nil", err)
	}
}

func TestValidateComposeWithCLI_InvalidCompose(t *testing.T) {
	if !dockerAvailable(t) {
		return
	}
	// Invalid: service references non-existent build context
	invalidCompose := `name: test
services:
  app:
    build: ./nonexistent-directory-that-does-not-exist
`
	err := validateComposeWithCLI(invalidCompose)
	if err == nil {
		t.Error("validateComposeWithCLI(invalid compose) = nil, want error")
	}
	if err != nil && !strings.Contains(err.Error(), "invalid compose") {
		t.Errorf("validateComposeWithCLI error should mention 'invalid compose', got: %v", err)
	}
}

func TestValidateComposeWithCLI_InvalidYAML(t *testing.T) {
	if !dockerAvailable(t) {
		return
	}
	// Invalid YAML - docker compose config will reject it
	invalidCompose := `name: test
services:
  app:
    image: [broken yaml
`
	err := validateComposeWithCLI(invalidCompose)
	if err == nil {
		t.Error("validateComposeWithCLI(invalid YAML) = nil, want error")
	}
}

func TestValidateComposeContent_ValidCompose(t *testing.T) {
	if !dockerAvailable(t) {
		return
	}
	cfg := &config.Config{
		Infrastructure: config.InfrastructureConfig{
			DefaultProxyNetwork: "proxy",
		},
	}
	s := &Server{config: cfg}

	validCompose := `name: test
services:
  app:
    image: nginx:alpine
    networks:
      - proxy
networks:
  proxy:
    external: true
`
	err := s.validateComposeContent(validCompose, "test")
	if err != nil {
		t.Errorf("validateComposeContent(valid) = %v, want nil", err)
	}
}

func TestValidateComposeContent_RejectsInvalidYAML(t *testing.T) {
	cfg := &config.Config{
		Infrastructure: config.InfrastructureConfig{
			DefaultProxyNetwork: "proxy",
		},
	}
	s := &Server{config: cfg}

	err := s.validateComposeContent("not valid: [ yaml", "test")
	if err == nil {
		t.Error("validateComposeContent(invalid YAML) = nil, want error")
	}
	if err != nil && !strings.Contains(err.Error(), "invalid YAML") {
		t.Errorf("error should mention YAML, got: %v", err)
	}
}

func TestValidateComposeContent_RejectsEmptyServices(t *testing.T) {
	cfg := &config.Config{
		Infrastructure: config.InfrastructureConfig{
			DefaultProxyNetwork: "proxy",
		},
	}
	s := &Server{config: cfg}

	err := s.validateComposeContent("name: test\nservices: {}", "test")
	if err == nil {
		t.Error("validateComposeContent(no services) = nil, want error")
	}
	if err != nil && !strings.Contains(err.Error(), "at least one service") {
		t.Errorf("error should mention services, got: %v", err)
	}
}
