package api

import (
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/config"
	"gopkg.in/yaml.v3"
)

func TestUpdateComposePorts(t *testing.T) {
	s := &Server{}

	tests := []struct {
		name           string
		inputCompose   string
		ports          []PortConfig
		expectPorts    []string
		expectExpose   []string
		expectNoChange bool
	}{
		{
			name: "single exposed port",
			inputCompose: `name: test
services:
  app:
    image: nginx
`,
			ports:        []PortConfig{{ContainerPort: 80, HostPort: ""}},
			expectExpose: []string{"80"},
		},
		{
			name: "single mapped port",
			inputCompose: `name: test
services:
  app:
    image: nginx
`,
			ports:       []PortConfig{{ContainerPort: 80, HostPort: "8080"}},
			expectPorts: []string{"8080:80"},
		},
		{
			name: "multiple ports mixed",
			inputCompose: `name: test
services:
  app:
    image: nginx
`,
			ports: []PortConfig{
				{ContainerPort: 80, HostPort: "8080"},
				{ContainerPort: 443, HostPort: ""},
				{ContainerPort: 3000, HostPort: "3000"},
			},
			expectPorts:  []string{"8080:80", "3000:3000"},
			expectExpose: []string{"443"},
		},
		{
			name: "replaces existing ports",
			inputCompose: `name: test
services:
  app:
    image: nginx
    ports:
      - "9000:9000"
    expose:
      - "9001"
`,
			ports:        []PortConfig{{ContainerPort: 80, HostPort: ""}},
			expectExpose: []string{"80"},
		},
		{
			name: "invalid yaml returns unchanged",
			inputCompose: `not valid yaml: [`,
			ports:          []PortConfig{{ContainerPort: 80, HostPort: ""}},
			expectNoChange: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := s.updateComposePorts(tt.inputCompose, tt.ports)

			if tt.expectNoChange {
				if result != tt.inputCompose {
					t.Errorf("expected unchanged content, got %s", result)
				}
				return
			}

			var compose map[string]interface{}
			if err := yaml.Unmarshal([]byte(result), &compose); err != nil {
				t.Fatalf("failed to parse result: %v", err)
			}

			services, ok := compose["services"].(map[string]interface{})
			if !ok {
				t.Fatal("services not found in result")
			}

			app, ok := services["app"].(map[string]interface{})
			if !ok {
				t.Fatal("app service not found")
			}

			if tt.expectPorts != nil {
				ports, ok := app["ports"].([]interface{})
				if !ok {
					t.Fatal("ports not found when expected")
				}
				if len(ports) != len(tt.expectPorts) {
					t.Errorf("expected %d ports, got %d", len(tt.expectPorts), len(ports))
				}
				for i, expected := range tt.expectPorts {
					if ports[i].(string) != expected {
						t.Errorf("port %d: expected %s, got %s", i, expected, ports[i])
					}
				}
			} else {
				if _, ok := app["ports"]; ok {
					t.Error("ports found when not expected")
				}
			}

			if tt.expectExpose != nil {
				expose, ok := app["expose"].([]interface{})
				if !ok {
					t.Fatal("expose not found when expected")
				}
				if len(expose) != len(tt.expectExpose) {
					t.Errorf("expected %d expose, got %d", len(tt.expectExpose), len(expose))
				}
				for i, expected := range tt.expectExpose {
					if expose[i].(string) != expected {
						t.Errorf("expose %d: expected %s, got %s", i, expected, expose[i])
					}
				}
			} else {
				if _, ok := app["expose"]; ok {
					t.Error("expose found when not expected")
				}
			}
		})
	}
}

func TestUpdateComposeDatabase(t *testing.T) {
	cfg := &config.Config{
		Infrastructure: config.InfrastructureConfig{
			DefaultProxyNetwork: "proxy",
		},
	}
	s := &Server{config: cfg}

	tests := []struct {
		name         string
		inputCompose string
		db           *DatabaseConfig
		expectDB     bool
		dbImage      string
	}{
		{
			name: "adds mysql database service",
			inputCompose: `name: test
services:
  app:
    image: nginx
`,
			db: &DatabaseConfig{
				Type:         "mysql",
				Mode:         "create",
				Name:         "testdb",
				User:         "testuser",
				Password:     "testpass",
				RootPassword: "rootpass",
			},
			expectDB: true,
			dbImage:  "mysql:8",
		},
		{
			name: "adds postgres database service",
			inputCompose: `name: test
services:
  app:
    image: nginx
`,
			db: &DatabaseConfig{
				Type:     "postgres",
				Mode:     "create",
				Name:     "testdb",
				User:     "testuser",
				Password: "testpass",
			},
			expectDB: true,
			dbImage:  "postgres:15",
		},
		{
			name: "adds mariadb database service",
			inputCompose: `name: test
services:
  app:
    image: nginx
`,
			db: &DatabaseConfig{
				Type:         "mariadb",
				Mode:         "create",
				Name:         "testdb",
				User:         "testuser",
				Password:     "testpass",
				RootPassword: "rootpass",
			},
			expectDB: true,
			dbImage:  "mariadb:10",
		},
		{
			name: "skips for existing mode",
			inputCompose: `name: test
services:
  app:
    image: nginx
`,
			db: &DatabaseConfig{
				Type: "mysql",
				Mode: "existing",
			},
			expectDB: false,
		},
		{
			name: "skips for external mode",
			inputCompose: `name: test
services:
  app:
    image: nginx
`,
			db: &DatabaseConfig{
				Type: "mysql",
				Mode: "external",
			},
			expectDB: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := s.updateComposeDatabase(tt.inputCompose, tt.db)

			var compose map[string]interface{}
			if err := yaml.Unmarshal([]byte(result), &compose); err != nil {
				t.Fatalf("failed to parse result: %v", err)
			}

			services, ok := compose["services"].(map[string]interface{})
			if !ok {
				t.Fatal("services not found")
			}

			db, hasDB := services["db"]
			if tt.expectDB && !hasDB {
				t.Error("expected db service but not found")
				return
			}
			if !tt.expectDB && hasDB {
				t.Error("db service found when not expected")
				return
			}

			if tt.expectDB {
				dbService := db.(map[string]interface{})
				image := dbService["image"].(string)
				if !strings.HasPrefix(image, strings.Split(tt.dbImage, ":")[0]) {
					t.Errorf("expected image prefix %s, got %s", tt.dbImage, image)
				}

				if _, hasVolumes := compose["volumes"]; !hasVolumes {
					t.Error("expected volumes section for database")
				}
			}
		})
	}
}

func TestCreateDatabaseService(t *testing.T) {
	cfg := &config.Config{
		Infrastructure: config.InfrastructureConfig{
			DefaultProxyNetwork: "proxy",
		},
	}
	s := &Server{config: cfg}

	tests := []struct {
		name        string
		db          *DatabaseConfig
		expectImage string
		expectEnv   map[string]string
	}{
		{
			name: "mysql service",
			db: &DatabaseConfig{
				Type:         "mysql",
				Name:         "mydb",
				User:         "myuser",
				Password:     "mypass",
				RootPassword: "rootpass",
			},
			expectImage: "mysql:8",
			expectEnv: map[string]string{
				"MYSQL_DATABASE":      "mydb",
				"MYSQL_USER":          "myuser",
				"MYSQL_PASSWORD":      "mypass",
				"MYSQL_ROOT_PASSWORD": "rootpass",
			},
		},
		{
			name: "postgres service",
			db: &DatabaseConfig{
				Type:     "postgres",
				Name:     "pgdb",
				User:     "pguser",
				Password: "pgpass",
			},
			expectImage: "postgres:15",
			expectEnv: map[string]string{
				"POSTGRES_DB":       "pgdb",
				"POSTGRES_USER":     "pguser",
				"POSTGRES_PASSWORD": "pgpass",
			},
		},
		{
			name: "mariadb service",
			db: &DatabaseConfig{
				Type:         "mariadb",
				Name:         "mariadb",
				User:         "mariauser",
				Password:     "mariapass",
				RootPassword: "rootpass",
			},
			expectImage: "mariadb:10",
			expectEnv: map[string]string{
				"MYSQL_DATABASE":      "mariadb",
				"MYSQL_USER":          "mariauser",
				"MYSQL_PASSWORD":      "mariapass",
				"MYSQL_ROOT_PASSWORD": "rootpass",
			},
		},
		{
			name: "unknown type returns nil",
			db: &DatabaseConfig{
				Type: "oracle",
			},
			expectImage: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := s.createDatabaseService(tt.db)

			if tt.expectImage == "" {
				if result != nil {
					t.Error("expected nil for unknown db type")
				}
				return
			}

			if result == nil {
				t.Fatal("expected service but got nil")
			}

			image := result["image"].(string)
			if image != tt.expectImage {
				t.Errorf("expected image %s, got %s", tt.expectImage, image)
			}

			env := result["environment"].(map[string]string)
			for key, expected := range tt.expectEnv {
				if env[key] != expected {
					t.Errorf("env %s: expected %s, got %s", key, expected, env[key])
				}
			}
		})
	}
}
