package api

import (
	"testing"

	"gopkg.in/yaml.v3"
)

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
