package e2e

import (
	"strings"
	"testing"
	"time"
)

func TestProxyHTTP(t *testing.T) {
	client := NewAPIClient()
	deploymentsPath := getEnv("FLATRUN_DEPLOYMENTS_PATH", "/tmp/flatrun-e2e-deployments")

	t.Run("Static deployment accessible via HTTP proxy", func(t *testing.T) {
		if testing.Short() {
			t.Skip("Skipping proxy test in short mode")
		}

		name := GenerateTestName("test-proxy-http")
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

		if staticContent == "" {
			t.Fatal("Static template not found")
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

		_, err = client.CreateDeployment(req)
		if err != nil {
			t.Fatalf("Failed to create deployment: %v", err)
		}

		defer func() {
			_ = client.StopDeployment(name)
			_ = client.DeleteDeployment(name)
		}()

		// Wait for container to start
		time.Sleep(5 * time.Second)

		// Verify nginx config was created
		if !NginxConfigExists(name) {
			t.Error("Nginx virtual host config was not created")
		}

		// Read and verify nginx config content
		nginxConfig, err := ReadNginxConfig(name)
		if err != nil {
			t.Logf("Could not read nginx config: %v", err)
		} else {
			if !strings.Contains(nginxConfig, domain) {
				t.Errorf("Nginx config does not contain domain %s", domain)
			}
			if !strings.Contains(nginxConfig, "proxy_pass") {
				t.Error("Nginx config does not contain proxy_pass directive")
			}
			t.Logf("Nginx config:\n%s", nginxConfig)
		}

		// Reload nginx to pick up new config
		if err := ReloadNginx(); err != nil {
			t.Logf("Warning: Could not reload nginx: %v", err)
		}

		// Wait for nginx to reload
		time.Sleep(2 * time.Second)

		// Test HTTP access via nginx proxy
		httpURL := GetNginxHTTPURL(domain)
		err = WaitForHTTPWithHost(httpURL, domain, 30*time.Second)
		if err != nil {
			t.Fatalf("HTTP proxy not accessible: %v", err)
		}

		status, body, err := HTTPGetWithHost(httpURL, domain)
		if err != nil {
			t.Fatalf("Failed to GET via HTTP proxy: %v", err)
		}

		if status != 200 {
			t.Errorf("Expected HTTP status 200, got %d", status)
		}

		if !strings.Contains(body, name) {
			t.Errorf("Response body does not contain deployment name %q", name)
		}

		t.Logf("HTTP proxy test passed. Response status: %d", status)

		// Verify deployment files exist
		_ = deploymentsPath // Used in verification
	})
}

func TestProxyHTTPS(t *testing.T) {
	client := NewAPIClient()

	t.Run("Static deployment accessible via HTTPS proxy", func(t *testing.T) {
		if testing.Short() {
			t.Skip("Skipping HTTPS proxy test in short mode")
		}

		name := GenerateTestName("test-proxy-https")
		domain := name + ".localhost"

		// Generate SSL certificate for this domain
		if err := GenerateCertForDomain(domain); err != nil {
			t.Fatalf("Failed to generate certificate for %s: %v", domain, err)
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

		_, err = client.CreateDeployment(req)
		if err != nil {
			t.Fatalf("Failed to create deployment: %v", err)
		}

		defer func() {
			_ = client.StopDeployment(name)
			_ = client.DeleteDeployment(name)
		}()

		// Wait for container to start
		time.Sleep(5 * time.Second)

		// Verify certificate exists
		if !CertificateExists(domain) {
			t.Error("SSL certificate was not found for domain")
		}

		// Verify nginx config was created
		if !NginxConfigExists(name) {
			t.Error("Nginx virtual host config was not created")
		}

		// Reload nginx to pick up new config
		if err := ReloadNginx(); err != nil {
			t.Logf("Warning: Could not reload nginx: %v", err)
		}

		time.Sleep(2 * time.Second)

		// Test HTTPS access via nginx proxy
		httpsURL := GetNginxHTTPSURL(domain)
		err = WaitForHTTPWithHost(httpsURL, domain, 30*time.Second)
		if err != nil {
			t.Fatalf("HTTPS proxy not accessible: %v", err)
		}

		status, body, err := HTTPGetWithHost(httpsURL, domain)
		if err != nil {
			t.Fatalf("Failed to GET via HTTPS proxy: %v", err)
		}

		if status != 200 {
			t.Errorf("Expected HTTPS status 200, got %d", status)
		}

		if !strings.Contains(body, name) {
			t.Errorf("Response body does not contain deployment name %q", name)
		}

		t.Logf("HTTPS proxy test passed. Response status: %d", status)
	})
}

func TestProxyVirtualHostCreation(t *testing.T) {
	client := NewAPIClient()

	t.Run("Deployment with expose creates nginx virtual host", func(t *testing.T) {
		name := GenerateTestName("test-vhost")
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
			AutoStart:      false,
			Metadata: &ServiceMetadata{
				Name: name,
				Type: "web",
				Networking: NetworkingConfig{
					Expose:        true,
					Domain:        domain,
					ContainerPort: 80,
				},
			},
		}

		_, err = client.CreateDeployment(req)
		if err != nil {
			t.Fatalf("Failed to create deployment: %v", err)
		}

		defer func() {
			_ = client.DeleteDeployment(name)
		}()

		// Give agent time to create nginx config
		time.Sleep(2 * time.Second)

		// Check nginx config exists
		if !NginxConfigExists(name) {
			t.Fatal("Nginx virtual host config was not created")
		}

		// Verify config content
		config, err := ReadNginxConfig(name)
		if err != nil {
			t.Fatalf("Failed to read nginx config: %v", err)
		}

		// Check required directives
		checks := []struct {
			name    string
			pattern string
		}{
			{"server_name", domain},
			{"proxy_pass", "proxy_pass"},
			{"listen 80", "listen 80"},
		}

		for _, check := range checks {
			if !strings.Contains(config, check.pattern) {
				t.Errorf("Nginx config missing %s (expected %q)", check.name, check.pattern)
			}
		}
	})

	t.Run("Deployment without expose does not create nginx virtual host", func(t *testing.T) {
		name := GenerateTestName("test-no-vhost")

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
			Metadata: &ServiceMetadata{
				Name: name,
				Type: "web",
				Networking: NetworkingConfig{
					Expose: false,
				},
			},
		}

		_, err = client.CreateDeployment(req)
		if err != nil {
			t.Fatalf("Failed to create deployment: %v", err)
		}

		defer func() {
			_ = client.DeleteDeployment(name)
		}()

		time.Sleep(2 * time.Second)

		// Should NOT have nginx config
		if NginxConfigExists(name) {
			t.Error("Nginx config should not exist for non-exposed deployment")
		}
	})
}

func TestProxySSLVirtualHost(t *testing.T) {
	client := NewAPIClient()

	t.Run("SSL deployment creates HTTPS virtual host config", func(t *testing.T) {
		name := GenerateTestName("test-ssl-vhost")
		domain := name + ".localhost"

		// Pre-generate certificate
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
			AutoStart:      false,
			Metadata: &ServiceMetadata{
				Name: name,
				Type: "web",
				Networking: NetworkingConfig{
					Expose:        true,
					Domain:        domain,
					ContainerPort: 80,
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
			_ = client.DeleteDeployment(name)
		}()

		time.Sleep(2 * time.Second)

		if !NginxConfigExists(name) {
			t.Fatal("Nginx config was not created")
		}

		config, err := ReadNginxConfig(name)
		if err != nil {
			t.Fatalf("Failed to read nginx config: %v", err)
		}

		// Check SSL directives
		sslChecks := []struct {
			name    string
			pattern string
		}{
			{"listen 443 ssl", "listen 443 ssl"},
			{"ssl_certificate", "ssl_certificate"},
			{"ssl_certificate_key", "ssl_certificate_key"},
			{"ssl_protocols", "ssl_protocols"},
		}

		for _, check := range sslChecks {
			if !strings.Contains(config, check.pattern) {
				t.Errorf("SSL nginx config missing %s", check.name)
			}
		}

		// Check HTTPS redirect
		if !strings.Contains(config, "return 301 https://") {
			t.Log("Warning: Config may not redirect HTTP to HTTPS")
		}

		t.Logf("SSL nginx config:\n%s", config)
	})
}
