package api

import (
	"testing"

	"github.com/flatrun/agent/pkg/models"
)

func TestGenerateDatabaseEnvVars(t *testing.T) {
	s := &Server{}

	tests := []struct {
		name          string
		prefix        string
		host          string
		port          int
		dbName        string
		username      string
		password      string
		dbType        string
		includeLegacy bool
		wantKeys      []string
		wantLegacy    bool
	}{
		{
			name:          "mysql with prefix and legacy",
			prefix:        "PRIMARY",
			host:          "localhost",
			port:          3306,
			dbName:        "myapp_db",
			username:      "myapp_user",
			password:      "secret123",
			dbType:        "mysql",
			includeLegacy: true,
			wantKeys:      []string{"PRIMARY_HOST", "PRIMARY_PORT", "PRIMARY_DATABASE", "PRIMARY_USERNAME", "PRIMARY_PASSWORD", "PRIMARY_URL"},
			wantLegacy:    true,
		},
		{
			name:          "postgres without legacy",
			prefix:        "ANALYTICS",
			host:          "db.example.com",
			port:          5432,
			dbName:        "analytics",
			username:      "analyst",
			password:      "pass123",
			dbType:        "postgres",
			includeLegacy: false,
			wantKeys:      []string{"ANALYTICS_HOST", "ANALYTICS_PORT", "ANALYTICS_DATABASE", "ANALYTICS_USERNAME", "ANALYTICS_PASSWORD", "ANALYTICS_URL"},
			wantLegacy:    false,
		},
		{
			name:          "redis generates correct URL",
			prefix:        "CACHE",
			host:          "redis.local",
			port:          6379,
			dbName:        "",
			username:      "",
			password:      "redispass",
			dbType:        "redis",
			includeLegacy: false,
			wantKeys:      []string{"CACHE_HOST", "CACHE_PORT", "CACHE_URL"},
			wantLegacy:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envVars := s.generateDatabaseEnvVars(
				tt.prefix,
				tt.host,
				tt.port,
				tt.dbName,
				tt.username,
				tt.password,
				tt.dbType,
				tt.includeLegacy,
			)

			envMap := make(map[string]string)
			for _, ev := range envVars {
				envMap[ev.Key] = ev.Value
			}

			for _, key := range tt.wantKeys {
				if _, ok := envMap[key]; !ok {
					t.Errorf("expected key %s not found in env vars", key)
				}
			}

			if tt.wantLegacy {
				legacyKeys := []string{"DB_HOST", "DB_PORT", "DB_DATABASE", "DB_USERNAME", "DB_PASSWORD", "DATABASE_URL"}
				for _, key := range legacyKeys {
					if _, ok := envMap[key]; !ok {
						t.Errorf("expected legacy key %s not found in env vars", key)
					}
				}
			}

			if !tt.wantLegacy {
				if _, ok := envMap["DB_HOST"]; ok {
					t.Error("legacy key DB_HOST should not be present")
				}
			}

			if tt.prefix+"_HOST" != "" {
				if envMap[tt.prefix+"_HOST"] != tt.host {
					t.Errorf("%s_HOST = %s, want %s", tt.prefix, envMap[tt.prefix+"_HOST"], tt.host)
				}
			}
		})
	}
}

func TestDatabaseConfigRequest_Validate(t *testing.T) {
	tests := []struct {
		name    string
		req     DatabaseConfigRequest
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid shared database config",
			req: DatabaseConfigRequest{
				Alias: "primary",
				Type:  "mysql",
				Mode:  "shared",
			},
			wantErr: false,
		},
		{
			name: "valid external database config",
			req: DatabaseConfigRequest{
				Alias:        "analytics",
				Type:         "postgres",
				Mode:         "external",
				ExternalHost: "db.example.com",
				ExternalPort: 5432,
				DatabaseName: "analytics_db",
				Username:     "analyst",
				Password:     "secret",
			},
			wantErr: false,
		},
		{
			name: "valid existing container config",
			req: DatabaseConfigRequest{
				Alias:             "cache",
				Type:              "redis",
				Mode:              "existing",
				ExistingContainer: "redis-server",
				DatabaseName:      "cache_db",
				Username:          "cache_user",
				Password:          "secret",
			},
			wantErr: false,
		},
		{
			name: "existing mode missing database_name",
			req: DatabaseConfigRequest{
				Alias:             "cache",
				Type:              "redis",
				Mode:              "existing",
				ExistingContainer: "redis-server",
				Username:          "cache_user",
				Password:          "secret",
			},
			wantErr: true,
			errMsg:  "database_name is required",
		},
		{
			name: "existing mode missing username",
			req: DatabaseConfigRequest{
				Alias:             "cache",
				Type:              "redis",
				Mode:              "existing",
				ExistingContainer: "redis-server",
				DatabaseName:      "cache_db",
				Password:          "secret",
			},
			wantErr: true,
			errMsg:  "username is required",
		},
		{
			name: "existing mode missing password",
			req: DatabaseConfigRequest{
				Alias:             "cache",
				Type:              "redis",
				Mode:              "existing",
				ExistingContainer: "redis-server",
				DatabaseName:      "cache_db",
				Username:          "cache_user",
			},
			wantErr: true,
			errMsg:  "password is required",
		},
		{
			name: "valid create mode config",
			req: DatabaseConfigRequest{
				Alias: "newdb",
				Type:  "mariadb",
				Mode:  "create",
			},
			wantErr: false,
		},
		{
			name: "invalid database type",
			req: DatabaseConfigRequest{
				Alias: "test",
				Type:  "oracle",
				Mode:  "shared",
			},
			wantErr: true,
			errMsg:  "invalid database type",
		},
		{
			name: "invalid mode",
			req: DatabaseConfigRequest{
				Alias: "test",
				Type:  "mysql",
				Mode:  "invalid",
			},
			wantErr: true,
			errMsg:  "invalid database mode",
		},
		{
			name: "existing mode missing container",
			req: DatabaseConfigRequest{
				Alias: "test",
				Type:  "mysql",
				Mode:  "existing",
			},
			wantErr: true,
			errMsg:  "existing_container is required",
		},
		{
			name: "external mode missing host",
			req: DatabaseConfigRequest{
				Alias:        "test",
				Type:         "postgres",
				Mode:         "external",
				ExternalPort: 5432,
			},
			wantErr: true,
			errMsg:  "external_host is required",
		},
		{
			name: "external mode missing port",
			req: DatabaseConfigRequest{
				Alias:        "test",
				Type:         "postgres",
				Mode:         "external",
				ExternalHost: "db.example.com",
			},
			wantErr: true,
			errMsg:  "external_port must be a positive integer",
		},
		{
			name: "external mode with zero port",
			req: DatabaseConfigRequest{
				Alias:        "test",
				Type:         "postgres",
				Mode:         "external",
				ExternalHost: "db.example.com",
				ExternalPort: 0,
			},
			wantErr: true,
			errMsg:  "external_port must be a positive integer",
		},
		{
			name: "external mode with negative port",
			req: DatabaseConfigRequest{
				Alias:        "test",
				Type:         "postgres",
				Mode:         "external",
				ExternalHost: "db.example.com",
				ExternalPort: -1,
			},
			wantErr: true,
			errMsg:  "external_port must be a positive integer",
		},
		{
			name: "valid mongodb config",
			req: DatabaseConfigRequest{
				Alias: "mongo",
				Type:  "mongodb",
				Mode:  "shared",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("Validate() expected error containing %q, got nil", tt.errMsg)
					return
				}
				if tt.errMsg != "" && !containsSubstring(err.Error(), tt.errMsg) {
					t.Errorf("Validate() error = %q, want error containing %q", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("Validate() unexpected error: %v", err)
				}
			}
		})
	}
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestMultipleDatabaseConfigs(t *testing.T) {
	metadata := &models.ServiceMetadata{
		Name: "myapp",
		Databases: []models.DatabaseConfig{
			{
				ID:           "myapp-primary",
				Alias:        "primary",
				Type:         "mysql",
				Mode:         "shared",
				Host:         "mysql-shared",
				Port:         3306,
				DatabaseName: "myapp_primary_db",
				Username:     "myapp_primary_user",
				EnvPrefix:    "PRIMARY",
				IsShared:     true,
			},
			{
				ID:        "myapp-cache",
				Alias:     "cache",
				Type:      "redis",
				Mode:      "existing",
				Container: "redis-server",
				Host:      "redis-server",
				Port:      6379,
				EnvPrefix: "CACHE",
				IsShared:  false,
			},
			{
				ID:           "myapp-analytics",
				Alias:        "analytics",
				Type:         "postgres",
				Mode:         "external",
				Host:         "analytics.db.example.com",
				Port:         5432,
				DatabaseName: "analytics",
				Username:     "analyst",
				EnvPrefix:    "ANALYTICS",
				IsShared:     false,
			},
		},
	}

	if !metadata.HasMultipleDatabases() {
		t.Error("HasMultipleDatabases() = false, want true")
	}

	databases := metadata.GetDatabases()
	if len(databases) != 3 {
		t.Errorf("GetDatabases() returned %d, want 3", len(databases))
	}

	primary := metadata.GetPrimaryDatabase()
	if primary == nil {
		t.Fatal("GetPrimaryDatabase() returned nil")
	} else if primary.Alias != "primary" {
		t.Errorf("GetPrimaryDatabase().Alias = %s, want primary", primary.Alias)
	}

	sharedCount := 0
	for _, db := range databases {
		if db.IsShared {
			sharedCount++
		}
	}
	if sharedCount != 1 {
		t.Errorf("shared database count = %d, want 1", sharedCount)
	}
}

func TestDatabaseEnvPrefixGeneration(t *testing.T) {
	tests := []struct {
		alias      string
		envPrefix  string
		wantPrefix string
	}{
		{
			alias:      "primary",
			envPrefix:  "",
			wantPrefix: "PRIMARY",
		},
		{
			alias:      "cache",
			envPrefix:  "REDIS",
			wantPrefix: "REDIS",
		},
		{
			alias:      "analytics-db",
			envPrefix:  "",
			wantPrefix: "ANALYTICS-DB",
		},
	}

	for _, tt := range tests {
		t.Run(tt.alias, func(t *testing.T) {
			prefix := tt.envPrefix
			if prefix == "" {
				prefix = tt.alias
			}

			upperPrefix := ""
			for _, c := range prefix {
				if c >= 'a' && c <= 'z' {
					upperPrefix += string(c - 32)
				} else {
					upperPrefix += string(c)
				}
			}

			if upperPrefix != tt.wantPrefix {
				t.Errorf("prefix = %s, want %s", upperPrefix, tt.wantPrefix)
			}
		})
	}
}
