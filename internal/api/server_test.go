package api

import (
	"os"
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/config"
	"gopkg.in/yaml.v3"
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

func TestAddDatabaseNetworkPreservesMultilineEnv(t *testing.T) {
	cfg := &config.Config{
		Infrastructure: config.InfrastructureConfig{
			DefaultDatabaseNetwork: "database",
		},
	}
	s := &Server{config: cfg}

	input := `name: test-wp
services:
  wordpress:
    image: wordpress:latest
    environment:
      WORDPRESS_DB_HOST: db
      WORDPRESS_CONFIG_EXTRA: |
        /* Handle HTTPS */
        if (isset($_SERVER['HTTP_X_FORWARDED_PROTO'])) {
            $_SERVER['HTTPS'] = 'on';
        }
    networks:
      - proxy
networks:
  proxy:
    external: true
`

	result := s.addDatabaseNetwork(input)

	if !strings.Contains(result, "database") {
		t.Error("database network should be added")
	}
	if !strings.Contains(result, "WORDPRESS_CONFIG_EXTRA") {
		t.Error("multiline environment variable should be preserved")
	}
	if !strings.Contains(result, "HTTP_X_FORWARDED_PROTO") {
		t.Error("multiline content should be preserved")
	}
}

func TestAddDatabaseNetworkWithMapStyleNetworks(t *testing.T) {
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
      proxy:
        aliases:
          - myapp
networks:
  proxy:
    external: true
`

	result := s.addDatabaseNetwork(input)

	if !strings.Contains(result, "database") {
		t.Error("database network should be added")
	}
	if !strings.Contains(result, "proxy") {
		t.Error("existing proxy network should be preserved")
	}
}

func TestSharedDatabaseNetworkIntegration(t *testing.T) {
	tests := []struct {
		name              string
		useSharedDatabase bool
		dbEnabled         bool
		dbType            string
		wantNetwork       bool
	}{
		{
			name:              "adds network when shared db enabled and local container",
			useSharedDatabase: true,
			dbEnabled:         true,
			dbType:            "container",
			wantNetwork:       true,
		},
		{
			name:              "no network when shared db disabled",
			useSharedDatabase: false,
			dbEnabled:         true,
			dbType:            "container",
			wantNetwork:       false,
		},
		{
			name:              "no network when db infrastructure disabled",
			useSharedDatabase: true,
			dbEnabled:         false,
			dbType:            "container",
			wantNetwork:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				DeploymentsPath: t.TempDir(),
				Infrastructure: config.InfrastructureConfig{
					DefaultDatabaseNetwork: "database",
					Database: config.SharedDatabaseConfig{
						Enabled: tt.dbEnabled,
						Type:    tt.dbType,
					},
				},
			}

			composeContent := `name: test-app
services:
  app:
    image: nginx
    networks:
      - proxy
networks:
  proxy:
    external: true
`

			var result string
			if tt.useSharedDatabase && cfg.Infrastructure.Database.Enabled {
				s := &Server{config: cfg}
				result = s.addDatabaseNetwork(composeContent)
			} else {
				result = composeContent
			}

			hasDbNetwork := strings.Contains(result, "database")
			if hasDbNetwork != tt.wantNetwork {
				t.Errorf("wantNetwork=%v, got hasDbNetwork=%v\nResult:\n%s",
					tt.wantNetwork, hasDbNetwork, result)
			}
		})
	}
}

func TestAddDatabaseNetworkOutputIsValidYAML(t *testing.T) {
	cfg := &config.Config{
		Infrastructure: config.InfrastructureConfig{
			DefaultDatabaseNetwork: "database",
		},
	}
	s := &Server{config: cfg}

	input := `name: test-app
services:
  app:
    image: nginx:alpine
    container_name: test-app
    environment:
      - FOO=bar
      - BAZ=qux
    volumes:
      - ./data:/var/data
    expose:
      - "80"
    networks:
      - proxy
    restart: unless-stopped

networks:
  proxy:
    external: true
`

	result := s.addDatabaseNetwork(input)

	var parsed map[string]interface{}
	if err := yaml.Unmarshal([]byte(result), &parsed); err != nil {
		t.Errorf("output is not valid YAML: %v\nOutput:\n%s", err, result)
	}

	services, ok := parsed["services"].(map[string]interface{})
	if !ok {
		t.Fatal("services section missing")
	}

	app, ok := services["app"].(map[string]interface{})
	if !ok {
		t.Fatal("app service missing")
	}

	networks, ok := app["networks"].([]interface{})
	if !ok {
		t.Fatal("networks should be a list")
	}

	hasProxy := false
	hasDatabase := false
	for _, n := range networks {
		if ns, ok := n.(string); ok {
			if ns == "proxy" {
				hasProxy = true
			}
			if ns == "database" {
				hasDatabase = true
			}
		}
	}

	if !hasProxy {
		t.Error("proxy network should be preserved")
	}
	if !hasDatabase {
		t.Error("database network should be added")
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

func TestInjectMounts(t *testing.T) {
	cfg := &config.Config{}
	s := &Server{config: cfg}

	availableMounts := []TemplateMount{
		{ID: "app", Name: "Application", ContainerPath: "/app", Type: "file"},
		{ID: "data", Name: "Data", ContainerPath: "/var/data", Type: "volume"},
		{ID: "config", Name: "Config", ContainerPath: "/etc/config", Type: "file"},
	}

	tests := []struct {
		name           string
		composeContent string
		selections     []MountSelection
		wantVolumes    []string
		dontWant       []string
	}{
		{
			name: "injects single bind mount",
			composeContent: `name: test
services:
  app:
    image: nginx
    expose:
      - "80"
`,
			selections: []MountSelection{
				{ID: "app", Enabled: true, Type: "file"},
			},
			wantVolumes: []string{"./app:/app"},
		},
		{
			name: "injects named volume",
			composeContent: `name: test
services:
  app:
    image: nginx
`,
			selections: []MountSelection{
				{ID: "data", Enabled: true, Type: "volume"},
			},
			wantVolumes: []string{"data_data:/var/data"},
		},
		{
			name: "injects multiple mounts",
			composeContent: `name: test
services:
  app:
    image: nginx
`,
			selections: []MountSelection{
				{ID: "app", Enabled: true, Type: "file"},
				{ID: "config", Enabled: true, Type: "file"},
			},
			wantVolumes: []string{"./app:/app", "./config:/etc/config"},
		},
		{
			name: "skips disabled mounts",
			composeContent: `name: test
services:
  app:
    image: nginx
`,
			selections: []MountSelection{
				{ID: "app", Enabled: true, Type: "file"},
				{ID: "data", Enabled: false, Type: "volume"},
			},
			wantVolumes: []string{"./app:/app"},
			dontWant:    []string{"data_data:/var/data"},
		},
		{
			name: "preserves existing volumes and adds new ones",
			composeContent: `name: test
services:
  app:
    image: nginx
    volumes:
      - ./existing:/existing
`,
			selections: []MountSelection{
				{ID: "app", Enabled: true, Type: "file"},
			},
			wantVolumes: []string{"./existing:/existing", "./app:/app"},
		},
		{
			name: "no injection when all mounts disabled",
			composeContent: `name: test
services:
  app:
    image: nginx
    volumes:
      - ./existing:/existing
`,
			selections: []MountSelection{
				{ID: "app", Enabled: false, Type: "file"},
				{ID: "data", Enabled: false, Type: "volume"},
			},
			wantVolumes: []string{"./existing:/existing"},
			dontWant:    []string{"./app:/app", "data_data:/var/data"},
		},
		{
			name: "handles unknown mount ID gracefully",
			composeContent: `name: test
services:
  app:
    image: nginx
`,
			selections: []MountSelection{
				{ID: "unknown", Enabled: true, Type: "file"},
				{ID: "app", Enabled: true, Type: "file"},
			},
			wantVolumes: []string{"./app:/app"},
			dontWant:    []string{"unknown"},
		},
		{
			name: "empty selections returns original content",
			composeContent: `name: test
services:
  app:
    image: nginx
`,
			selections:  []MountSelection{},
			wantVolumes: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := s.injectMounts(tt.composeContent, tt.selections, availableMounts)

			for _, want := range tt.wantVolumes {
				if !strings.Contains(result, want) {
					t.Errorf("expected volume %q in result:\n%s", want, result)
				}
			}

			for _, dontWant := range tt.dontWant {
				if strings.Contains(result, dontWant) {
					t.Errorf("unexpected volume %q in result:\n%s", dontWant, result)
				}
			}
		})
	}
}

func TestInjectMountsNoDuplicates(t *testing.T) {
	cfg := &config.Config{}
	s := &Server{config: cfg}

	availableMounts := []TemplateMount{
		{ID: "app", Name: "Application", ContainerPath: "/app", Type: "file"},
	}

	composeContent := `name: test
services:
  app:
    image: nginx
    volumes:
      - ./app:/app
`
	selections := []MountSelection{
		{ID: "app", Enabled: true, Type: "file"},
	}

	result := s.injectMounts(composeContent, selections, availableMounts)

	count := strings.Count(result, "./app:/app")
	if count > 1 {
		t.Errorf("expected no duplicate mounts, found %d occurrences of './app:/app':\n%s", count, result)
	}
}

func TestInjectMountsPreservesYAMLStructure(t *testing.T) {
	cfg := &config.Config{}
	s := &Server{config: cfg}

	availableMounts := []TemplateMount{
		{ID: "app", Name: "Application", ContainerPath: "/app", Type: "file"},
	}

	composeContent := `name: test
services:
  app:
    image: nginx
    environment:
      - FOO=bar
    expose:
      - "80"
    networks:
      - proxy
networks:
  proxy:
    external: true
`
	selections := []MountSelection{
		{ID: "app", Enabled: true, Type: "file"},
	}

	result := s.injectMounts(composeContent, selections, availableMounts)

	if !strings.Contains(result, "environment") {
		t.Error("environment section should be preserved")
	}
	if !strings.Contains(result, "FOO") {
		t.Error("environment variables should be preserved")
	}
	if !strings.Contains(result, "networks") {
		t.Error("networks section should be preserved")
	}
	if !strings.Contains(result, "external: true") {
		t.Error("network external flag should be preserved")
	}
	if !strings.Contains(result, "./app:/app") {
		t.Error("volume should be injected")
	}
}

func TestInjectMountsInvalidYAML(t *testing.T) {
	cfg := &config.Config{}
	s := &Server{config: cfg}

	availableMounts := []TemplateMount{
		{ID: "app", Name: "Application", ContainerPath: "/app", Type: "file"},
	}

	invalidContent := `this is not valid yaml: {{{`
	selections := []MountSelection{
		{ID: "app", Enabled: true, Type: "file"},
	}

	result := s.injectMounts(invalidContent, selections, availableMounts)

	if result != invalidContent {
		t.Error("invalid YAML should return original content unchanged")
	}
}

func TestInjectMountsNoServicesSection(t *testing.T) {
	cfg := &config.Config{}
	s := &Server{config: cfg}

	availableMounts := []TemplateMount{
		{ID: "app", Name: "Application", ContainerPath: "/app", Type: "file"},
	}

	contentWithoutServices := `name: test
networks:
  proxy:
    external: true
`
	selections := []MountSelection{
		{ID: "app", Enabled: true, Type: "file"},
	}

	result := s.injectMounts(contentWithoutServices, selections, availableMounts)

	if result != contentWithoutServices {
		t.Error("content without services should return original content unchanged")
	}
}

func TestGenerateComposeWithMountInjection(t *testing.T) {
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
    image: node:20-alpine
    container_name: ${NAME}
    expose:
      - "3000"
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
container_port: 3000
mounts:
  - id: app
    name: Application
    container_path: /app
    type: file
  - id: data
    name: Data Storage
    container_path: /var/data
    type: volume
`
	if err := writeFile(templateDir+"/metadata.yml", metadata); err != nil {
		t.Fatalf("failed to write metadata: %v", err)
	}

	opts := &ComposeGenerateRequest{
		Name:          "my-node-app",
		ContainerPort: 3000,
		Mounts: []MountSelection{
			{ID: "app", Enabled: true, Type: "file"},
			{ID: "data", Enabled: true, Type: "volume"},
		},
	}

	result, err := s.generateComposeWithOptions("test-app", opts)
	if err != nil {
		t.Fatalf("generateComposeWithOptions failed: %v", err)
	}

	if !strings.Contains(result, "my-node-app") {
		t.Error("Result should contain deployment name")
	}
	if !strings.Contains(result, "./app:/app") {
		t.Error("Result should contain bind mount for app")
	}
	if !strings.Contains(result, "data_data:/var/data") {
		t.Error("Result should contain named volume for data")
	}
}

func TestGenerateComposeWithNoMountsSelected(t *testing.T) {
	cfg := &config.Config{
		DeploymentsPath: t.TempDir(),
		Infrastructure: config.InfrastructureConfig{
			DefaultProxyNetwork: "proxy",
		},
	}
	s := &Server{config: cfg}

	templateDir := cfg.DeploymentsPath + "/.flatrun/templates/stateless-app"
	if err := createDir(templateDir); err != nil {
		t.Fatalf("failed to create template dir: %v", err)
	}

	composeContent := `name: ${NAME}
services:
  app:
    image: nginx:alpine
    container_name: ${NAME}
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

	metadata := `name: Stateless App
container_port: 80
mounts:
  - id: html
    name: HTML Content
    container_path: /usr/share/nginx/html
    type: file
    required: false
`
	if err := writeFile(templateDir+"/metadata.yml", metadata); err != nil {
		t.Fatalf("failed to write metadata: %v", err)
	}

	opts := &ComposeGenerateRequest{
		Name:          "my-stateless-app",
		ContainerPort: 80,
		Mounts: []MountSelection{
			{ID: "html", Enabled: false, Type: "file"},
		},
	}

	result, err := s.generateComposeWithOptions("stateless-app", opts)
	if err != nil {
		t.Fatalf("generateComposeWithOptions failed: %v", err)
	}

	if strings.Contains(result, "volumes:") {
		t.Error("Result should not contain volumes section when all mounts disabled")
	}
	if strings.Contains(result, "./html") {
		t.Error("Result should not contain disabled mount")
	}
}

func TestProxySyncResultStructure(t *testing.T) {
	result := ProxySyncResult{
		Name:    "test-deployment",
		Domain:  "test.example.com",
		Success: true,
		Message: "Created",
		Created: true,
	}

	if result.Name != "test-deployment" {
		t.Errorf("expected name 'test-deployment', got '%s'", result.Name)
	}
	if result.Domain != "test.example.com" {
		t.Errorf("expected domain 'test.example.com', got '%s'", result.Domain)
	}
	if !result.Success {
		t.Error("expected Success to be true")
	}
	if !result.Created {
		t.Error("expected Created to be true")
	}
}

func TestProxySyncResultSkipped(t *testing.T) {
	result := ProxySyncResult{
		Name:    "existing-deployment",
		Domain:  "existing.example.com",
		Success: true,
		Message: "Already exists",
		Created: false,
	}

	if !result.Success {
		t.Error("expected Success to be true for skipped")
	}
	if result.Created {
		t.Error("expected Created to be false for skipped")
	}
	if result.Message != "Already exists" {
		t.Errorf("expected message 'Already exists', got '%s'", result.Message)
	}
}

func TestProxySyncResultFailed(t *testing.T) {
	result := ProxySyncResult{
		Name:    "failed-deployment",
		Domain:  "failed.example.com",
		Success: false,
		Message: "connection refused",
		Created: false,
	}

	if result.Success {
		t.Error("expected Success to be false for failed")
	}
	if result.Created {
		t.Error("expected Created to be false for failed")
	}
}
