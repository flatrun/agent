package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStaticDeployment(t *testing.T) {
	client := NewAPIClient()
	deploymentsPath := getEnv("FLATRUN_DEPLOYMENTS_PATH", "/tmp/flatrun-e2e-deployments")

	// Ensure templates are synced before running tests
	if err := client.RefreshTemplates(); err != nil {
		t.Fatalf("Failed to refresh templates: %v", err)
	}

	t.Run("API creates HTML file from template", func(t *testing.T) {
		name := GenerateTestName("test-static")

		templates, err := client.GetTemplates()
		if err != nil {
			t.Fatalf("Failed to get templates: %v", err)
		}

		var staticContent string
		for _, tmpl := range templates.Templates {
			if tmpl.ID == "static" {
				staticContent = tmpl.Content
				break
			}
		}

		if staticContent == "" {
			t.Fatal("Static template not found")
		}

		composeContent := strings.ReplaceAll(staticContent, "${NAME}", name)

		req := CreateDeploymentRequest{
			Name:           name,
			ComposeContent: composeContent,
			TemplateID:     "static",
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

		// Verify API created the HTML file
		htmlPath := filepath.Join(deploymentsPath, name, "html", "index.html")
		content, err := os.ReadFile(htmlPath)
		if err != nil {
			t.Fatalf("API did not create HTML file at %s: %v", htmlPath, err)
		}

		htmlContent := string(content)

		if strings.Contains(htmlContent, "${NAME}") {
			t.Error("API did not replace ${NAME} placeholder in HTML")
		}

		if !strings.Contains(htmlContent, name) {
			t.Errorf("HTML does not contain deployment name %q", name)
		}

		if !strings.Contains(strings.ToLower(htmlContent), "flatrun") {
			t.Error("HTML does not contain FlatRun branding")
		}
	})

	t.Run("API creates proxy config and serves HTTP", func(t *testing.T) {
		if testing.Short() {
			t.Skip("Skipping HTTP serve test in short mode")
		}

		name := GenerateTestName("test-static-http")
		domain := name + ".localhost"

		templates, err := client.GetTemplates()
		if err != nil {
			t.Fatalf("Failed to get templates: %v", err)
		}

		var staticContent string
		for _, tmpl := range templates.Templates {
			if tmpl.ID == "static" {
				staticContent = tmpl.Content
				break
			}
		}

		composeContent := strings.ReplaceAll(staticContent, "${NAME}", name)

		req := CreateDeploymentRequest{
			Name:           name,
			ComposeContent: composeContent,
			TemplateID:     "static",
			AutoStart:      true,
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

		resp, err := client.CreateDeployment(req)
		if err != nil {
			t.Fatalf("Failed to create deployment: %v", err)
		}

		defer func() {
			_ = client.StopDeployment(name)
			_ = client.DeleteDeployment(name)
		}()

		// Verify API response includes proxy setup
		if resp.ProxyResult == nil {
			t.Fatal("API did not return proxy_result")
		}

		if !resp.ProxyResult.Success {
			t.Errorf("API proxy setup failed: %s", resp.ProxyResult.Message)
		}

		if !resp.ProxyResult.VirtualHostCreated {
			t.Error("API did not create virtual host")
		}

		// Verify nginx config was created by API
		if !NginxConfigExists(name) {
			t.Fatal("API did not create nginx config")
		}

		// Wait for container to start and reload nginx
		time.Sleep(3 * time.Second)
		_ = ReloadNginx()
		time.Sleep(2 * time.Second)

		// Test HTTP access
		httpURL := GetNginxHTTPURL(domain)
		err = WaitForHTTPWithHost(httpURL, domain, 30*time.Second)
		if err != nil {
			t.Fatalf("HTTP access failed: %v", err)
		}

		status, body, err := HTTPGetWithHost(httpURL, domain)
		if err != nil {
			t.Fatalf("Failed to GET %s: %v", httpURL, err)
		}

		if status != 200 {
			t.Errorf("Expected status 200, got %d", status)
		}

		if !strings.Contains(body, name) {
			t.Errorf("Response body does not contain deployment name %q", name)
		}
	})

	t.Run("API creates SSL proxy config and serves HTTPS", func(t *testing.T) {
		if testing.Short() {
			t.Skip("Skipping HTTPS serve test in short mode")
		}

		name := GenerateTestName("test-static-https")
		domain := name + ".localhost"

		// Pre-generate certificate (simulating existing cert)
		if err := GenerateCertForDomain(domain); err != nil {
			t.Fatalf("Failed to generate certificate: %v", err)
		}

		templates, err := client.GetTemplates()
		if err != nil {
			t.Fatalf("Failed to get templates: %v", err)
		}

		var staticContent string
		for _, tmpl := range templates.Templates {
			if tmpl.ID == "static" {
				staticContent = tmpl.Content
				break
			}
		}

		composeContent := strings.ReplaceAll(staticContent, "${NAME}", name)

		req := CreateDeploymentRequest{
			Name:           name,
			ComposeContent: composeContent,
			TemplateID:     "static",
			AutoStart:      true,
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

		resp, err := client.CreateDeployment(req)
		if err != nil {
			t.Fatalf("Failed to create deployment: %v", err)
		}

		defer func() {
			_ = client.StopDeployment(name)
			_ = client.DeleteDeployment(name)
		}()

		// Verify API response
		if resp.ProxyResult == nil {
			t.Fatal("API did not return proxy_result")
		}

		if !resp.ProxyResult.Success {
			t.Errorf("API proxy setup failed: %s", resp.ProxyResult.Message)
		}

		// Verify nginx config has SSL configuration
		if !NginxConfigExists(name) {
			t.Fatal("API did not create nginx config")
		}

		config, _ := ReadNginxConfig(name)
		if !strings.Contains(config, "listen 443 ssl") {
			t.Error("API did not create SSL configuration in nginx config")
		}

		// Wait for container to start and reload nginx
		time.Sleep(3 * time.Second)
		_ = ReloadNginx()
		time.Sleep(2 * time.Second)

		// Test HTTPS access
		httpsURL := GetNginxHTTPSURL(domain)
		err = WaitForHTTPWithHost(httpsURL, domain, 30*time.Second)
		if err != nil {
			t.Fatalf("HTTPS access failed: %v", err)
		}

		status, body, err := HTTPGetWithHost(httpsURL, domain)
		if err != nil {
			t.Fatalf("Failed to GET %s: %v", httpsURL, err)
		}

		if status != 200 {
			t.Errorf("Expected HTTPS status 200, got %d", status)
		}

		if !strings.Contains(body, name) {
			t.Errorf("Response body does not contain deployment name %q", name)
		}
	})
}

func TestStaticDeploymentComposeContent(t *testing.T) {
	client := NewAPIClient()
	deploymentsPath := getEnv("FLATRUN_DEPLOYMENTS_PATH", "/tmp/flatrun-e2e-deployments")

	if err := client.RefreshTemplates(); err != nil {
		t.Fatalf("Failed to refresh templates: %v", err)
	}

	name := GenerateTestName("test-static-compose")

	templates, err := client.GetTemplates()
	if err != nil {
		t.Fatalf("Failed to get templates: %v", err)
	}

	var staticContent string
	for _, tmpl := range templates.Templates {
		if tmpl.ID == "static" {
			staticContent = tmpl.Content
			break
		}
	}

	composeContent := strings.ReplaceAll(staticContent, "${NAME}", name)

	req := CreateDeploymentRequest{
		Name:           name,
		ComposeContent: composeContent,
		TemplateID:     "static",
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
		t.Fatalf("API did not create compose file: %v", err)
	}

	composeStr := string(content)

	t.Run("Compose has correct name", func(t *testing.T) {
		if !strings.Contains(composeStr, fmt.Sprintf("name: %s", name)) {
			t.Error("Compose file missing correct name")
		}
	})

	t.Run("Compose has container_name", func(t *testing.T) {
		if !strings.Contains(composeStr, fmt.Sprintf("container_name: %s", name)) {
			t.Error("Compose file missing container_name")
		}
	})

	t.Run("Compose uses expose not ports", func(t *testing.T) {
		if strings.Contains(composeStr, "ports:") {
			t.Error("Compose file should use expose, not ports")
		}
		if !strings.Contains(composeStr, "expose:") {
			t.Error("Compose file missing expose directive")
		}
	})

	t.Run("Compose has web network", func(t *testing.T) {
		if !strings.Contains(composeStr, "networks:") {
			t.Error("Compose file missing networks section")
		}
		if !strings.Contains(composeStr, "web:") {
			t.Error("Compose file missing web network")
		}
	})
}
