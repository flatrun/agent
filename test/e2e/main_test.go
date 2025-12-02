package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

var useDocker = os.Getenv("E2E_USE_DOCKER") != "false"

const (
	deploymentsPath = "/tmp/flatrun-e2e-deployments"
	certsPath       = "/tmp/flatrun-e2e-certs"
)

func TestMain(m *testing.M) {
	if os.Getenv("FLATRUN_API_URL") != "" {
		// External agent provided, just run tests
		os.Exit(m.Run())
	}

	if !useDocker {
		fmt.Println("No FLATRUN_API_URL set and E2E_USE_DOCKER=false")
		fmt.Println("Please set FLATRUN_API_URL or allow Docker test environment")
		os.Exit(1)
	}

	// Start Docker test environment
	fmt.Println("Starting test environment...")
	if err := startTestEnvironment(); err != nil {
		fmt.Printf("Failed to start test environment: %v\n", err)
		os.Exit(1)
	}

	// Set the API URL and deployments path for tests
	os.Setenv("FLATRUN_API_URL", "http://localhost:18090/api")
	os.Setenv("FLATRUN_DEPLOYMENTS_PATH", deploymentsPath)
	os.Setenv("FLATRUN_CERTS_PATH", certsPath)
	baseURL = "http://localhost:18090/api"

	// Wait for agent to be ready
	fmt.Println("Waiting for agent to be ready...")
	if err := waitForAgent(); err != nil {
		fmt.Printf("Agent failed to start: %v\n", err)
		stopTestEnvironment()
		os.Exit(1)
	}

	fmt.Println("Running tests...")
	code := m.Run()

	// Cleanup
	fmt.Println("Stopping test environment...")
	stopTestEnvironment()

	os.Exit(code)
}

func startTestEnvironment() error {
	// Clean and create directories
	_ = os.RemoveAll(deploymentsPath)
	_ = os.RemoveAll(certsPath)

	if err := os.MkdirAll(deploymentsPath+"/nginx/conf.d", 0755); err != nil {
		return fmt.Errorf("failed to create deployments directory: %w", err)
	}
	if err := os.MkdirAll(certsPath+"/live/default", 0755); err != nil {
		return fmt.Errorf("failed to create certs directory: %w", err)
	}

	// Generate default SSL certificate for nginx
	if err := generateDefaultCert(); err != nil {
		return fmt.Errorf("failed to generate default certificate: %w", err)
	}

	cmd := exec.Command("docker", "compose", "-f", "docker-compose.test.yml", "up", "-d", "--build")
	cmd.Dir = getTestDir()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func generateDefaultCert() error {
	certDir := certsPath + "/live/default"
	cmd := exec.Command("openssl", "req", "-x509", "-nodes", "-days", "365",
		"-newkey", "rsa:2048",
		"-keyout", certDir+"/privkey.pem",
		"-out", certDir+"/fullchain.pem",
		"-subj", "/CN=localhost",
		"-addext", "subjectAltName=DNS:localhost,DNS:*.localhost")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func stopTestEnvironment() {
	cmd := exec.Command("docker", "compose", "-f", "docker-compose.test.yml", "down", "-v", "--remove-orphans")
	cmd.Dir = getTestDir()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()

	// Clean up directories
	_ = os.RemoveAll(deploymentsPath)
	_ = os.RemoveAll(certsPath)
}

func waitForAgent() error {
	client := NewAPIClient()
	deadline := time.Now().Add(90 * time.Second)

	for time.Now().Before(deadline) {
		resp, err := client.Get("/health")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("timeout waiting for agent")
}

func getTestDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}
