package models

import (
	"testing"
)

func TestServiceMetadata_GetDatabases(t *testing.T) {
	tests := []struct {
		name     string
		metadata ServiceMetadata
		want     int
	}{
		{
			name:     "empty databases",
			metadata: ServiceMetadata{},
			want:     0,
		},
		{
			name: "single database",
			metadata: ServiceMetadata{
				Databases: []DatabaseConfig{
					{ID: "db1", Alias: "primary", Type: "mysql"},
				},
			},
			want: 1,
		},
		{
			name: "multiple databases",
			metadata: ServiceMetadata{
				Databases: []DatabaseConfig{
					{ID: "db1", Alias: "primary", Type: "mysql"},
					{ID: "db2", Alias: "cache", Type: "redis"},
					{ID: "db3", Alias: "analytics", Type: "postgres"},
				},
			},
			want: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.metadata.GetDatabases()
			if len(got) != tt.want {
				t.Errorf("GetDatabases() returned %d databases, want %d", len(got), tt.want)
			}
		})
	}
}

func TestServiceMetadata_GetPrimaryDatabase(t *testing.T) {
	tests := []struct {
		name      string
		metadata  ServiceMetadata
		wantAlias string
		wantNil   bool
	}{
		{
			name:     "empty databases returns nil",
			metadata: ServiceMetadata{},
			wantNil:  true,
		},
		{
			name: "returns database with primary alias",
			metadata: ServiceMetadata{
				Databases: []DatabaseConfig{
					{ID: "db1", Alias: "cache", Type: "redis"},
					{ID: "db2", Alias: "primary", Type: "mysql"},
					{ID: "db3", Alias: "analytics", Type: "postgres"},
				},
			},
			wantAlias: "primary",
		},
		{
			name: "returns first database if no primary alias",
			metadata: ServiceMetadata{
				Databases: []DatabaseConfig{
					{ID: "db1", Alias: "cache", Type: "redis"},
					{ID: "db2", Alias: "main", Type: "mysql"},
				},
			},
			wantAlias: "cache",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.metadata.GetPrimaryDatabase()
			if tt.wantNil {
				if got != nil {
					t.Errorf("GetPrimaryDatabase() = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Error("GetPrimaryDatabase() returned nil, want non-nil")
				return
			}
			if got.Alias != tt.wantAlias {
				t.Errorf("GetPrimaryDatabase().Alias = %s, want %s", got.Alias, tt.wantAlias)
			}
		})
	}
}

func TestServiceMetadata_HasMultipleDatabases(t *testing.T) {
	tests := []struct {
		name     string
		metadata ServiceMetadata
		want     bool
	}{
		{
			name:     "empty databases",
			metadata: ServiceMetadata{},
			want:     false,
		},
		{
			name: "single database",
			metadata: ServiceMetadata{
				Databases: []DatabaseConfig{
					{ID: "db1", Alias: "primary", Type: "mysql"},
				},
			},
			want: false,
		},
		{
			name: "multiple databases",
			metadata: ServiceMetadata{
				Databases: []DatabaseConfig{
					{ID: "db1", Alias: "primary", Type: "mysql"},
					{ID: "db2", Alias: "cache", Type: "redis"},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.metadata.HasMultipleDatabases(); got != tt.want {
				t.Errorf("HasMultipleDatabases() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestServiceMetadata_EffectivePrimaryService(t *testing.T) {
	tests := []struct {
		name string
		meta ServiceMetadata
		want string
	}{
		{"user pin wins", ServiceMetadata{PrimaryService: "api", Networking: NetworkingConfig{Service: "web"}}, "api"},
		{"falls back to networking service", ServiceMetadata{Networking: NetworkingConfig{Service: "web"}}, "web"},
		{"empty when neither set", ServiceMetadata{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.meta.EffectivePrimaryService(); got != tt.want {
				t.Errorf("EffectivePrimaryService() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServiceMetadata_GetDomains(t *testing.T) {
	tests := []struct {
		name        string
		metadata    ServiceMetadata
		want        int
		wantService string
	}{
		{
			name: "returns Domains array when present",
			metadata: ServiceMetadata{
				Domains: []DomainConfig{
					{ID: "d1", Domain: "example.com"},
					{ID: "d2", Domain: "api.example.com"},
				},
			},
			want: 2,
		},
		{
			name: "falls back to networking domain with service name",
			metadata: ServiceMetadata{
				Name: "myapp",
				Networking: NetworkingConfig{
					Expose:        true,
					Domain:        "myapp.example.com",
					Service:       "web",
					ContainerPort: 8080,
				},
				SSL: SSLConfig{Enabled: true},
			},
			want:        1,
			wantService: "web",
		},
		{
			name: "user-pinned primary service overrides the networking service",
			metadata: ServiceMetadata{
				Name:           "myapp",
				PrimaryService: "api",
				Networking: NetworkingConfig{
					Expose:        true,
					Domain:        "myapp.example.com",
					Service:       "web",
					ContainerPort: 8080,
				},
			},
			want:        1,
			wantService: "api",
		},
		{
			name: "falls back to metadata name when service is empty",
			metadata: ServiceMetadata{
				Name: "myapp",
				Networking: NetworkingConfig{
					Expose:        true,
					Domain:        "myapp.example.com",
					ContainerPort: 8080,
				},
				SSL: SSLConfig{Enabled: true},
			},
			want:        1,
			wantService: "myapp",
		},
		{
			name: "returns nil when not exposed",
			metadata: ServiceMetadata{
				Networking: NetworkingConfig{
					Expose: false,
				},
			},
			want: 0,
		},
		{
			name: "returns nil when domain is empty",
			metadata: ServiceMetadata{
				Networking: NetworkingConfig{
					Expose: true,
					Domain: "",
				},
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.metadata.GetDomains()
			if len(got) != tt.want {
				t.Errorf("GetDomains() returned %d domains, want %d", len(got), tt.want)
			}
			if tt.wantService != "" && len(got) > 0 {
				if got[0].Service != tt.wantService {
					t.Errorf("GetDomains()[0].Service = %q, want %q", got[0].Service, tt.wantService)
				}
			}
		})
	}
}

func TestServiceMetadata_GetUniqueDomainNames(t *testing.T) {
	tests := []struct {
		name     string
		metadata ServiceMetadata
		want     int
	}{
		{
			name: "returns unique domain names including aliases",
			metadata: ServiceMetadata{
				Domains: []DomainConfig{
					{
						ID:      "d1",
						Domain:  "example.com",
						Aliases: []string{"www.example.com"},
					},
					{
						ID:     "d2",
						Domain: "api.example.com",
					},
				},
			},
			want: 3,
		},
		{
			name: "deduplicates domains",
			metadata: ServiceMetadata{
				Domains: []DomainConfig{
					{
						ID:      "d1",
						Domain:  "example.com",
						Aliases: []string{"example.com"},
					},
				},
			},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.metadata.GetUniqueDomainNames()
			if len(got) != tt.want {
				t.Errorf("GetUniqueDomainNames() returned %d names, want %d", len(got), tt.want)
			}
		})
	}
}

func TestDatabaseConfig_Fields(t *testing.T) {
	db := DatabaseConfig{
		ID:           "test-db-1",
		Alias:        "primary",
		Type:         "mysql",
		Mode:         "shared",
		Service:      "backend",
		Host:         "localhost",
		Port:         3306,
		Container:    "mysql-container",
		DatabaseName: "myapp_db",
		Username:     "myapp_user",
		EnvPrefix:    "PRIMARY",
		IsShared:     true,
	}

	if db.ID != "test-db-1" {
		t.Errorf("ID = %s, want test-db-1", db.ID)
	}
	if db.Alias != "primary" {
		t.Errorf("Alias = %s, want primary", db.Alias)
	}
	if db.Type != "mysql" {
		t.Errorf("Type = %s, want mysql", db.Type)
	}
	if db.Mode != "shared" {
		t.Errorf("Mode = %s, want shared", db.Mode)
	}
	if db.Port != 3306 {
		t.Errorf("Port = %d, want 3306", db.Port)
	}
	if !db.IsShared {
		t.Error("IsShared = false, want true")
	}
}
