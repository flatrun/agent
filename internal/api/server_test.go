package api

import (
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
