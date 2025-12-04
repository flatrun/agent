package api

import (
	"os"
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/config"
)

func TestAddDatabaseNetwork(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		dbNetwork      string
		wantNetworkDef bool
		wantInService  bool
	}{
		{
			name: "adds database network to simple compose",
			input: `name: test-app
services:
  app:
    image: nginx
    networks:
      - proxy
networks:
  proxy:
    external: true
`,
			dbNetwork:      "database",
			wantNetworkDef: true,
			wantInService:  true,
		},
		{
			name: "adds database network when no networks defined",
			input: `name: test-app
services:
  app:
    image: nginx
`,
			dbNetwork:      "database",
			wantNetworkDef: true,
			wantInService:  true,
		},
		{
			name: "handles multiple services",
			input: `name: test-app
services:
  web:
    image: nginx
    networks:
      - proxy
  worker:
    image: redis
networks:
  proxy:
    external: true
`,
			dbNetwork:      "database",
			wantNetworkDef: true,
			wantInService:  true,
		},
		{
			name: "uses custom database network name",
			input: `name: test-app
services:
  app:
    image: nginx
`,
			dbNetwork:      "custom-db-net",
			wantNetworkDef: true,
			wantInService:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Infrastructure: config.InfrastructureConfig{
					DefaultDatabaseNetwork: tt.dbNetwork,
				},
			}
			s := &Server{config: cfg}

			result := s.addDatabaseNetwork(tt.input)

			// Check network definition exists
			if tt.wantNetworkDef {
				if !strings.Contains(result, tt.dbNetwork+":") {
					t.Errorf("expected network definition for %q, got:\n%s", tt.dbNetwork, result)
				}
				if !strings.Contains(result, "external: true") {
					t.Errorf("expected external: true in network definition, got:\n%s", result)
				}
			}

			// Check network is added to services
			if tt.wantInService {
				if !strings.Contains(result, "- "+tt.dbNetwork) {
					t.Errorf("expected service to have network %q, got:\n%s", tt.dbNetwork, result)
				}
			}
		})
	}
}

func TestAddDatabaseNetworkPreservesExisting(t *testing.T) {
	cfg := &config.Config{
		Infrastructure: config.InfrastructureConfig{
			DefaultDatabaseNetwork: "database",
		},
	}
	s := &Server{config: cfg}

	input := `name: test-app
services:
  app:
    image: nginx
    networks:
      - proxy
      - database
networks:
  proxy:
    external: true
  database:
    external: true
`

	result := s.addDatabaseNetwork(input)

	// Count occurrences of database network - should not duplicate
	count := strings.Count(result, "- database")
	if count != 1 {
		t.Errorf("expected exactly 1 occurrence of '- database' in service networks, got %d:\n%s", count, result)
	}
}

func TestProcessTemplateFilesWithEnvVars(t *testing.T) {
	cfg := &config.Config{
		DeploymentsPath: t.TempDir(),
	}
	s := &Server{config: cfg}

	templateDir := cfg.DeploymentsPath + "/.flatrun/templates/test-template"
	if err := createDir(templateDir); err != nil {
		t.Fatalf("failed to create template dir: %v", err)
	}

	metadata := `name: Test Template
files:
  - path: .env
    content: |
      APP_NAME=${NAME}
      DB_HOST=${DB_HOST}
      DB_PASSWORD=${DB_PASSWORD}
`
	if err := writeFile(templateDir+"/metadata.yml", metadata); err != nil {
		t.Fatalf("failed to write metadata: %v", err)
	}

	deploymentName := "my-app"
	deploymentDir := cfg.DeploymentsPath + "/" + deploymentName
	if err := createDir(deploymentDir); err != nil {
		t.Fatalf("failed to create deployment dir: %v", err)
	}

	envVars := []EnvVar{
		{Key: "DB_HOST", Value: "localhost"},
		{Key: "DB_PASSWORD", Value: "secret123"},
	}

	s.processTemplateFiles(deploymentName, "test-template", envVars)

	content, err := readFile(deploymentDir + "/.env")
	if err != nil {
		t.Fatalf("failed to read generated .env: %v", err)
	}

	if !strings.Contains(content, "APP_NAME=my-app") {
		t.Errorf("expected APP_NAME=my-app, got:\n%s", content)
	}
	if !strings.Contains(content, "DB_HOST=localhost") {
		t.Errorf("expected DB_HOST=localhost, got:\n%s", content)
	}
	if !strings.Contains(content, "DB_PASSWORD=secret123") {
		t.Errorf("expected DB_PASSWORD=secret123, got:\n%s", content)
	}
}

func createDir(path string) error {
	return os.MkdirAll(path, 0755)
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

func readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	return string(data), err
}
