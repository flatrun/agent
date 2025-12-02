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

	if len(templates) == 0 {
		t.Fatal("List() returned empty list, expected at least one template")
	}

	expectedTemplates := []string{"static", "wordpress", "laravel", "ghost", "nextjs", "astro", "node", "php"}
	templateMap := make(map[string]bool)
	for _, tmpl := range templates {
		templateMap[tmpl] = true
	}

	for _, expected := range expectedTemplates {
		if !templateMap[expected] {
			t.Errorf("List() missing expected template %q", expected)
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
			name:         "static template",
			templateID:   "static",
			wantName:     "Static Site",
			wantPriority: 100,
			wantErr:      false,
		},
		{
			name:         "wordpress template",
			templateID:   "wordpress",
			wantName:     "WordPress",
			wantPriority: 100,
			wantErr:      false,
		},
		{
			name:         "laravel template",
			templateID:   "laravel",
			wantName:     "Laravel",
			wantPriority: 80,
			wantErr:      false,
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
		name           string
		templateID     string
		wantContains   []string
		wantNotContain []string
		wantErr        bool
	}{
		{
			name:       "static template",
			templateID: "static",
			wantContains: []string{
				"name: ${NAME}",
				"nginx:alpine",
				"expose:",
				"networks:",
				"web:",
			},
			wantNotContain: []string{
				"ports:",
			},
			wantErr: false,
		},
		{
			name:       "wordpress template",
			templateID: "wordpress",
			wantContains: []string{
				"name: ${NAME}",
				"wordpress:",
				"WORDPRESS_DB_HOST",
				"expose:",
				"networks:",
			},
			wantErr: false,
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

			for _, notWant := range tt.wantNotContain {
				if strings.Contains(content, notWant) {
					t.Errorf("GetCompose() content should not contain %q", notWant)
				}
			}
		})
	}
}

func TestStaticTemplateHasFiles(t *testing.T) {
	data, err := GetMetadata("static")
	if err != nil {
		t.Fatalf("GetMetadata() error = %v", err)
	}

	var metadata struct {
		Files []struct {
			Path    string `yaml:"path"`
			Content string `yaml:"content"`
		} `yaml:"files"`
	}

	if err := yaml.Unmarshal(data, &metadata); err != nil {
		t.Fatalf("Failed to parse metadata: %v", err)
	}

	if len(metadata.Files) == 0 {
		t.Fatal("Static template should have files defined")
	}

	foundIndex := false
	for _, f := range metadata.Files {
		if f.Path == "html/index.html" {
			foundIndex = true
			if !strings.Contains(f.Content, "${NAME}") {
				t.Error("index.html should contain ${NAME} placeholder")
			}
			if !strings.Contains(f.Content, "flatrun") {
				t.Error("index.html should contain FlatRun branding")
			}
		}
	}

	if !foundIndex {
		t.Error("Static template should have html/index.html file")
	}
}
