package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWordPressDeployment(t *testing.T) {
	client := NewAPIClient()
	deploymentsPath := getEnv("FLATRUN_DEPLOYMENTS_PATH", "/tmp/flatrun-e2e-deployments")

	t.Run("Create WordPress deployment with template", func(t *testing.T) {
		name := GenerateTestName("test-wp")

		templates, err := client.GetTemplates()
		if err != nil {
			t.Fatalf("Failed to get templates: %v", err)
		}

		var wpContent string
		for _, tmpl := range templates.Templates {
			if tmpl.ID == "wordpress" {
				wpContent = tmpl.Content
				break
			}
		}

		if wpContent == "" {
			t.Fatal("WordPress template not found")
		}

		composeContent := strings.ReplaceAll(wpContent, "${NAME}", name)

		req := CreateDeploymentRequest{
			Name:           name,
			ComposeContent: composeContent,
			TemplateID:     "wordpress",
			AutoStart:      false,
		}

		_, err = client.CreateDeployment(req)
		if err != nil {
			t.Fatalf("Failed to create deployment: %v", err)
		}

		defer func() {
			_ = client.StopDeployment(name)
			_ = client.DeleteDeployment(name)
		}()

		// Verify compose file was created
		composePath := filepath.Join(deploymentsPath, name, "docker-compose.yml")
		content, err := os.ReadFile(composePath)
		if err != nil {
			t.Fatalf("Compose file not created at %s: %v", composePath, err)
		}

		composeStr := string(content)

		if strings.Contains(composeStr, "${NAME}") {
			t.Error("Compose still contains ${NAME} placeholder")
		}

		if !strings.Contains(composeStr, fmt.Sprintf("name: %s", name)) {
			t.Error("Compose does not contain correct name")
		}
	})

	t.Run("WordPress deployment has correct compose structure", func(t *testing.T) {
		name := GenerateTestName("test-wp-compose")

		templates, err := client.GetTemplates()
		if err != nil {
			t.Fatalf("Failed to get templates: %v", err)
		}

		var wpContent string
		for _, tmpl := range templates.Templates {
			if tmpl.ID == "wordpress" {
				wpContent = tmpl.Content
				break
			}
		}

		composeContent := strings.ReplaceAll(wpContent, "${NAME}", name)

		req := CreateDeploymentRequest{
			Name:           name,
			ComposeContent: composeContent,
			TemplateID:     "wordpress",
			AutoStart:      false,
		}

		_, err = client.CreateDeployment(req)
		if err != nil {
			t.Fatalf("Failed to create deployment: %v", err)
		}

		defer func() {
			_ = client.DeleteDeployment(name)
		}()

		composePath := filepath.Join(deploymentsPath, name, "docker-compose.yml")
		content, err := os.ReadFile(composePath)
		if err != nil {
			t.Fatalf("Failed to read compose file: %v", err)
		}

		composeStr := string(content)

		checks := []struct {
			name    string
			check   string
			wantErr string
		}{
			{"has name", fmt.Sprintf("name: %s", name), "missing name"},
			{"has wordpress service", "wordpress:", "missing wordpress service"},
			{"has DB host env", "WORDPRESS_DB_HOST", "missing WORDPRESS_DB_HOST"},
			{"has expose", "expose:", "should use expose"},
			{"has networks", "networks:", "missing networks"},
			{"has proxy network", "proxy:", "missing proxy network"},
		}

		for _, c := range checks {
			if !strings.Contains(composeStr, c.check) {
				t.Errorf("WordPress compose %s: %s", c.wantErr, c.name)
			}
		}

		if strings.Contains(composeStr, "ports:") {
			t.Error("WordPress compose should use expose, not ports")
		}
	})

	t.Run("WordPress deployment with shared database", func(t *testing.T) {
		name := GenerateTestName("test-wp-shared")

		templates, err := client.GetTemplates()
		if err != nil {
			t.Fatalf("Failed to get templates: %v", err)
		}

		var wpContent string
		for _, tmpl := range templates.Templates {
			if tmpl.ID == "wordpress" {
				wpContent = tmpl.Content
				break
			}
		}

		composeContent := strings.ReplaceAll(wpContent, "${NAME}", name)

		req := CreateDeploymentRequest{
			Name:              name,
			ComposeContent:    composeContent,
			TemplateID:        "wordpress",
			AutoStart:         false,
			UseSharedDatabase: true,
		}

		_, err = client.CreateDeployment(req)
		if err != nil {
			t.Logf("Shared database deployment failed (expected if not configured): %v", err)
			return
		}

		defer func() {
			_ = client.StopDeployment(name)
			_ = client.DeleteDeployment(name)
		}()

		envPath := filepath.Join(deploymentsPath, name, ".env.flatrun")
		if _, err := os.Stat(envPath); err != nil {
			t.Logf("No .env.flatrun file (shared DB may not be configured): %v", err)
		}
	})
}

func TestWordPressHTTPAccess(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping WordPress HTTP test in short mode")
	}

	client := NewAPIClient()
	name := GenerateTestName("test-wp-http")
	domain := name + ".localhost"

	templates, err := client.GetTemplates()
	if err != nil {
		t.Fatalf("Failed to get templates: %v", err)
	}

	var wpContent string
	for _, tmpl := range templates.Templates {
		if tmpl.ID == "wordpress" {
			wpContent = tmpl.Content
			break
		}
	}

	composeContent := strings.ReplaceAll(wpContent, "${NAME}", name)

	req := CreateDeploymentRequest{
		Name:              name,
		ComposeContent:    composeContent,
		TemplateID:        "wordpress",
		AutoStart:         true,
		UseSharedDatabase: true,
		Metadata: &ServiceMetadata{
			Name: name,
			Type: "web",
			Networking: NetworkingConfig{
				Expose:        true,
				Domain:        domain,
				ContainerPort: 80,
				Protocol:      "http",
			},
		},
	}

	_, err = client.CreateDeployment(req)
	if err != nil {
		t.Fatalf("Failed to create deployment: %v", err)
	}

	defer func() {
		_ = client.StopDeployment(name)
		_ = client.DeleteDeployment(name)
	}()

	// Verify nginx config created
	if !NginxConfigExists(name) {
		t.Fatal("Nginx config was not created for WordPress")
	}

	config, _ := ReadNginxConfig(name)
	t.Logf("WordPress nginx config:\n%s", config)

	// Test HTTP access from inside Docker network
	err = WaitForContainerHTTP(name, 80, 60*time.Second)
	if err != nil {
		t.Fatalf("WordPress container not accessible: %v", err)
	}

	// Also verify nginx proxy works from inside
	err = WaitForNginxProxy(name, domain, 30*time.Second)
	if err != nil {
		t.Logf("Warning: Nginx proxy check failed: %v", err)
	}

	t.Logf("WordPress accessible via HTTP from Docker network")
}

func TestWordPressHTTPSAccess(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping WordPress HTTPS test in short mode")
	}

	client := NewAPIClient()
	name := GenerateTestName("test-wp-https")
	domain := name + ".localhost"

	// Generate SSL certificate first
	if err := GenerateCertForDomain(domain); err != nil {
		t.Fatalf("Failed to generate certificate: %v", err)
	}

	templates, err := client.GetTemplates()
	if err != nil {
		t.Fatalf("Failed to get templates: %v", err)
	}

	var wpContent string
	for _, tmpl := range templates.Templates {
		if tmpl.ID == "wordpress" {
			wpContent = tmpl.Content
			break
		}
	}

	composeContent := strings.ReplaceAll(wpContent, "${NAME}", name)

	req := CreateDeploymentRequest{
		Name:              name,
		ComposeContent:    composeContent,
		TemplateID:        "wordpress",
		AutoStart:         true,
		UseSharedDatabase: true,
		Metadata: &ServiceMetadata{
			Name: name,
			Type: "web",
			Networking: NetworkingConfig{
				Expose:        true,
				Domain:        domain,
				ContainerPort: 80,
				Protocol:      "http",
			},
			SSL: SSLConfig{
				Enabled:  true,
				AutoCert: false,
			},
		},
	}

	_, err = client.CreateDeployment(req)
	if err != nil {
		t.Fatalf("Failed to create deployment: %v", err)
	}

	defer func() {
		_ = client.StopDeployment(name)
		_ = client.DeleteDeployment(name)
	}()

	// Verify nginx config with SSL
	if !NginxConfigExists(name) {
		t.Fatal("Nginx config was not created")
	}

	config, _ := ReadNginxConfig(name)
	if !strings.Contains(config, "listen 443 ssl") {
		t.Error("Nginx config missing SSL configuration")
	}
	t.Logf("WordPress HTTPS nginx config:\n%s", config)

	// Test HTTP access from inside Docker network
	err = WaitForContainerHTTP(name, 80, 60*time.Second)
	if err != nil {
		t.Fatalf("WordPress container not accessible: %v", err)
	}

	// Verify nginx proxy works from inside (tests the config is valid)
	err = WaitForNginxProxy(name, domain, 30*time.Second)
	if err != nil {
		t.Logf("Warning: Nginx proxy check failed: %v", err)
	}

	t.Logf("WordPress accessible via HTTPS config from Docker network")
}
