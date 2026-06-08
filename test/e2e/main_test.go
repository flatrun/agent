package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	testcontainers "github.com/testcontainers/testcontainers-go"
	tc "github.com/testcontainers/testcontainers-go/modules/compose"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	deploymentsPath = "/tmp/flatrun-e2e-deployments"
	certsPath       = "/tmp/flatrun-e2e-certs"
)

func TestMain(m *testing.M) {
	if os.Getenv("FLATRUN_API_URL") != "" {
		os.Exit(m.Run())
	}

	if os.Getenv("E2E_USE_DOCKER") == "false" {
		fmt.Println("No FLATRUN_API_URL set and E2E_USE_DOCKER=false")
		os.Exit(1)
	}

	ctx := context.Background()

	if err := prepareDirectories(); err != nil {
		fmt.Printf("Failed to prepare directories: %v\n", err)
		os.Exit(1)
	}

	if err := generateDefaultCert(); err != nil {
		fmt.Printf("Failed to generate default certificate: %v\n", err)
		os.Exit(1)
	}

	nw, nwErr := testcontainers.GenericNetwork(ctx, testcontainers.GenericNetworkRequest{ //nolint:staticcheck
		NetworkRequest: testcontainers.NetworkRequest{ //nolint:staticcheck
			Name:   "proxy",
			Driver: "bridge",
		},
	})
	if nwErr != nil && !strings.Contains(nwErr.Error(), "already exists") {
		fmt.Printf("Failed to create proxy network: %v\n", nwErr)
		os.Exit(1)
	}

	compose, err := tc.NewDockerCompose("docker-compose.test.yml")
	if err != nil {
		fmt.Printf("Failed to create compose environment: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Starting test environment...")
	err = compose.
		WaitForService("agent", wait.ForHTTP("/api/health").WithPort("8090/tcp").WithStartupTimeout(90*time.Second)).
		Up(ctx, tc.Wait(true))
	if err != nil {
		fmt.Printf("Failed to start test environment: %v\n", err)
		_ = compose.Down(ctx, tc.RemoveOrphans(true), tc.RemoveVolumes(true))
		os.Exit(1)
	}

	os.Setenv("FLATRUN_API_URL", "http://localhost:18090/api")
	os.Setenv("FLATRUN_DEPLOYMENTS_PATH", deploymentsPath)
	os.Setenv("FLATRUN_CERTS_PATH", certsPath)
	baseURL = "http://localhost:18090/api"

	fmt.Println("Running tests...")
	code := m.Run()

	fmt.Println("Stopping test environment...")
	if err := compose.Down(ctx, tc.RemoveOrphans(true), tc.RemoveVolumes(true)); err != nil {
		fmt.Printf("Failed to stop compose environment: %v\n", err)
	}
	if nw != nil {
		if err := nw.Remove(ctx); err != nil {
			fmt.Printf("Failed to remove proxy network: %v\n", err)
		}
	}
	if err := os.RemoveAll(deploymentsPath); err != nil {
		fmt.Printf("Failed to remove deployments directory: %v\n", err)
	}
	if err := os.RemoveAll(certsPath); err != nil {
		fmt.Printf("Failed to remove certs directory: %v\n", err)
	}

	os.Exit(code)
}

func prepareDirectories() error {
	// The agent container runs as root and leaves root-owned state behind;
	// starting on top of it makes unrelated tests fail in confusing ways.
	for _, dir := range []string{deploymentsPath, certsPath} {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("leftover state at %s cannot be removed (root-owned files from a previous run; remove the directory manually): %w", dir, err)
		}
	}

	if err := os.MkdirAll(deploymentsPath+"/nginx/conf.d", 0755); err != nil {
		return fmt.Errorf("failed to create deployments directory: %w", err)
	}
	if err := os.MkdirAll(certsPath+"/live/default", 0755); err != nil {
		return fmt.Errorf("failed to create certs directory: %w", err)
	}
	return nil
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
