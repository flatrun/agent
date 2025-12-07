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

func TestGenerateComposeWithOptions(t *testing.T) {
	cfg := &config.Config{
		DeploymentsPath: t.TempDir(),
		Infrastructure: config.InfrastructureConfig{
			DefaultProxyNetwork: "proxy",
		},
	}
	s := &Server{config: cfg}

	// Create template directory with compose file
	templateDir := cfg.DeploymentsPath + "/.flatrun/templates/wordpress"
	if err := createDir(templateDir); err != nil {
		t.Fatalf("failed to create template dir: %v", err)
	}

	// WordPress template with environment variables
	composeContent := `name: ${NAME}
services:
  wordpress:
    image: wordpress:latest
    container_name: ${NAME}
    environment:
      WORDPRESS_DB_HOST: ${DB_HOST:-db}
      WORDPRESS_DB_USER: ${DB_USERNAME:-wordpress}
      WORDPRESS_CONFIG_EXTRA: |
        /* Handle HTTPS behind reverse proxy */
        if (isset($_SERVER['HTTP_X_FORWARDED_PROTO'])) {
            $_SERVER['HTTPS'] = 'on';
        }
    expose:
      - "80"
    networks:
      - proxy
    restart: unless-stopped

networks:
  proxy:
    external: true
`
	if err := writeFile(templateDir+"/docker-compose.yml", composeContent); err != nil {
		t.Fatalf("failed to write compose: %v", err)
	}

	metadata := `name: WordPress
container_port: 80
`
	if err := writeFile(templateDir+"/metadata.yml", metadata); err != nil {
		t.Fatalf("failed to write metadata: %v", err)
	}

	opts := &ComposeGenerateRequest{
		Name:          "my-wordpress",
		ContainerPort: 80,
	}

	result, err := s.generateComposeWithOptions("wordpress", opts)
	if err != nil {
		t.Fatalf("generateComposeWithOptions failed: %v", err)
	}

	// Verify ${NAME} was substituted
	if strings.Contains(result, "${NAME}") {
		t.Error("${NAME} should be substituted")
	}
	if !strings.Contains(result, "my-wordpress") {
		t.Error("Result should contain deployment name 'my-wordpress'")
	}

	// Verify environment variables are preserved
	if !strings.Contains(result, "WORDPRESS_DB_HOST") {
		t.Error("Result should contain WORDPRESS_DB_HOST environment variable")
	}
	if !strings.Contains(result, "WORDPRESS_CONFIG_EXTRA") {
		t.Error("Result should contain WORDPRESS_CONFIG_EXTRA environment variable")
	}
	if !strings.Contains(result, "HTTP_X_FORWARDED_PROTO") {
		t.Error("Result should contain the HTTPS proxy fix code")
	}

	// Verify it's using the template image, not a generic one
	if !strings.Contains(result, "wordpress:latest") {
		t.Error("Result should use the template's image (wordpress:latest)")
	}
}

func TestGenerateComposeWithOptionsPortMapping(t *testing.T) {
	cfg := &config.Config{
		DeploymentsPath: t.TempDir(),
		Infrastructure: config.InfrastructureConfig{
			DefaultProxyNetwork: "proxy",
		},
	}
	s := &Server{config: cfg}

	templateDir := cfg.DeploymentsPath + "/.flatrun/templates/test-app"
	if err := createDir(templateDir); err != nil {
		t.Fatalf("failed to create template dir: %v", err)
	}

	composeContent := `name: ${NAME}
services:
  app:
    image: nginx:alpine
    expose:
      - "80"
    networks:
      - proxy

networks:
  proxy:
    external: true
`
	if err := writeFile(templateDir+"/docker-compose.yml", composeContent); err != nil {
		t.Fatalf("failed to write compose: %v", err)
	}

	metadata := `name: Test App
container_port: 80
`
	if err := writeFile(templateDir+"/metadata.yml", metadata); err != nil {
		t.Fatalf("failed to write metadata: %v", err)
	}

	opts := &ComposeGenerateRequest{
		Name:          "my-app",
		ContainerPort: 80,
		MapPorts:      true,
		HostPort:      "8080",
	}

	result, err := s.generateComposeWithOptions("test-app", opts)
	if err != nil {
		t.Fatalf("generateComposeWithOptions failed: %v", err)
	}

	// Verify port mapping was applied
	if !strings.Contains(result, "ports:") {
		t.Error("Result should contain ports: section when MapPorts is true")
	}
	if !strings.Contains(result, "8080:80") {
		t.Error("Result should contain port mapping '8080:80'")
	}
}

func TestGenerateComposeWithOptionsCustomTemplate(t *testing.T) {
	cfg := &config.Config{
		DeploymentsPath: t.TempDir(),
		Infrastructure: config.InfrastructureConfig{
			DefaultProxyNetwork: "proxy",
		},
	}
	s := &Server{config: cfg}

	opts := &ComposeGenerateRequest{
		Name:          "custom-app",
		ContainerPort: 3000,
	}

	// Test with "custom" template ID - should use generic template
	result, err := s.generateComposeWithOptions("custom", opts)
	if err != nil {
		t.Fatalf("generateComposeWithOptions failed: %v", err)
	}

	if !strings.Contains(result, "custom-app") {
		t.Error("Result should contain deployment name")
	}
}
