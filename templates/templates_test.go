package templates

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestList(t *testing.T) {
	templates, err := List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	templateMap := make(map[string]bool)
	for _, tmpl := range templates {
		templateMap[tmpl] = true
	}

	for _, expected := range []string{"infra/postgres", "infra/mysql", "infra/mariadb", "infra/redis", "infra/nginx"} {
		if !templateMap[expected] {
			t.Errorf("List() missing expected infra template %q", expected)
		}
	}

	// App templates now live outside the binary and must not be embedded.
	for _, gone := range []string{"wordpress", "laravel", "static", "ghost"} {
		if templateMap[gone] {
			t.Errorf("List() unexpectedly still embeds app template %q", gone)
		}
	}
}

func TestGetMetadata(t *testing.T) {
	tests := []struct {
		name         string
		templateID   string
		wantName     string
		wantPriority int
		wantErr      bool
	}{
		{
			name:         "postgres infra template",
			templateID:   "infra/postgres",
			wantName:     "PostgreSQL",
			wantPriority: 80,
		},
		{
			name:         "nginx infra template",
			templateID:   "infra/nginx",
			wantName:     "Nginx",
			wantPriority: 100,
		},
		{
			name:       "app template no longer embedded",
			templateID: "wordpress",
			wantErr:    true,
		},
		{
			name:       "non-existent template",
			templateID: "non-existent",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := GetMetadata(tt.templateID)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetMetadata() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}

			var metadata struct {
				Name     string `yaml:"name"`
				Priority int    `yaml:"priority"`
			}
			if err := yaml.Unmarshal(data, &metadata); err != nil {
				t.Fatalf("Failed to parse metadata: %v", err)
			}
			if metadata.Name != tt.wantName {
				t.Errorf("GetMetadata() name = %q, want %q", metadata.Name, tt.wantName)
			}
			if metadata.Priority != tt.wantPriority {
				t.Errorf("GetMetadata() priority = %d, want %d", metadata.Priority, tt.wantPriority)
			}
		})
	}
}

func TestGetCompose(t *testing.T) {
	tests := []struct {
		name         string
		templateID   string
		wantContains []string
		wantErr      bool
	}{
		{
			name:       "postgres infra template",
			templateID: "infra/postgres",
			wantContains: []string{
				"name: ${NAME}",
				"postgres:",
				"networks:",
			},
		},
		{
			name:       "app template no longer embedded",
			templateID: "wordpress",
			wantErr:    true,
		},
		{
			name:       "non-existent template",
			templateID: "non-existent",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := GetCompose(tt.templateID)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetCompose() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}

			content := string(data)
			for _, want := range tt.wantContains {
				if !strings.Contains(content, want) {
					t.Errorf("GetCompose() content missing %q", want)
				}
			}
		})
	}
}

func TestGetWelcomePage(t *testing.T) {
	data, err := GetWelcomePage()
	if err != nil {
		t.Fatalf("GetWelcomePage() error = %v", err)
	}
	if len(data) == 0 {
		t.Fatal("GetWelcomePage() returned empty content")
	}
}
