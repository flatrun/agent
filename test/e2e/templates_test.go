package e2e

import (
	"testing"
)

func TestTemplatesEndpoint(t *testing.T) {
	client := NewAPIClient()

	t.Run("GET /templates returns templates sorted by priority", func(t *testing.T) {
		resp, err := client.GetTemplates()
		if err != nil {
			t.Fatalf("GetTemplates() error = %v", err)
		}

		if len(resp.Templates) == 0 {
			t.Fatal("Expected at least one template")
		}

		// Check priority sorting (highest first)
		prevPriority := 999
		for _, tmpl := range resp.Templates {
			if tmpl.Priority > prevPriority {
				t.Errorf("Templates not sorted by priority: %s (priority %d) came after higher priority",
					tmpl.ID, tmpl.Priority)
			}
			prevPriority = tmpl.Priority
		}

		// Static and WordPress should be first (priority 100)
		foundHighPriority := false
		for i, tmpl := range resp.Templates {
			if i < 2 && (tmpl.ID == "static" || tmpl.ID == "wordpress") {
				foundHighPriority = true
				if tmpl.Priority != 100 {
					t.Errorf("Expected %s to have priority 100, got %d", tmpl.ID, tmpl.Priority)
				}
			}
		}

		if !foundHighPriority {
			t.Error("Expected static or wordpress to be in first 2 positions")
		}
	})

	t.Run("Templates have required fields", func(t *testing.T) {
		resp, err := client.GetTemplates()
		if err != nil {
			t.Fatalf("GetTemplates() error = %v", err)
		}

		for _, tmpl := range resp.Templates {
			if tmpl.ID == "" {
				t.Error("Template missing ID")
			}
			if tmpl.Name == "" {
				t.Errorf("Template %s missing Name", tmpl.ID)
			}
			if tmpl.Content == "" {
				t.Errorf("Template %s missing Content", tmpl.ID)
			}
		}
	})

	t.Run("POST /templates/refresh updates templates", func(t *testing.T) {
		err := client.RefreshTemplates()
		if err != nil {
			t.Fatalf("RefreshTemplates() error = %v", err)
		}

		// Verify templates still load correctly after refresh
		resp, err := client.GetTemplates()
		if err != nil {
			t.Fatalf("GetTemplates() after refresh error = %v", err)
		}

		if len(resp.Templates) == 0 {
			t.Fatal("No templates after refresh")
		}
	})
}

func TestTemplateContent(t *testing.T) {
	client := NewAPIClient()

	resp, err := client.GetTemplates()
	if err != nil {
		t.Fatalf("GetTemplates() error = %v", err)
	}

	templateMap := make(map[string]Template)
	for _, tmpl := range resp.Templates {
		templateMap[tmpl.ID] = tmpl
	}

	t.Run("Static template has correct content", func(t *testing.T) {
		tmpl, ok := templateMap["static"]
		if !ok {
			t.Fatal("Static template not found")
		}

		checks := []string{
			"name: ${NAME}",
			"nginx:alpine",
			"./html:/usr/share/nginx/html",
			"expose:",
			"networks:",
		}

		for _, check := range checks {
			if !containsString(tmpl.Content, check) {
				t.Errorf("Static template missing: %s", check)
			}
		}
	})

	t.Run("WordPress template has correct content", func(t *testing.T) {
		tmpl, ok := templateMap["wordpress"]
		if !ok {
			t.Fatal("WordPress template not found")
		}

		checks := []string{
			"name: ${NAME}",
			"wordpress:",
			"WORDPRESS_DB_HOST",
			"WORDPRESS_DB_USER",
			"expose:",
			"networks:",
		}

		for _, check := range checks {
			if !containsString(tmpl.Content, check) {
				t.Errorf("WordPress template missing: %s", check)
			}
		}
	})
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStringHelper(s, substr))
}

func containsStringHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
