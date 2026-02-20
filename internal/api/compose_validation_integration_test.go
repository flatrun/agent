package api

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/flatrun/agent/pkg/config"
)

var (
	dockerCheckOnce sync.Once
	dockerCheckErr  error
)

// dockerAvailable ensures docker is available and the proxy network exists for CLI validation tests.
// Runs the check once; subsequent calls use the cached result. Fails when Docker is unavailable.
func dockerAvailable(t *testing.T) {
	t.Helper()
	dockerCheckOnce.Do(func() {
		dockerCheckErr = checkDocker()
	})
	if dockerCheckErr != nil {
		t.Fatal(dockerCheckErr)
	}
}

func checkDocker() error {
	if testing.Short() {
		return fmt.Errorf("compose CLI validation tests require Docker; do not use -short")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker not in PATH: %w", err)
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		return fmt.Errorf("docker daemon not reachable: %w", err)
	}
	_ = exec.Command("docker", "network", "create", "proxy").Run()
	if err := exec.Command("docker", "network", "inspect", "proxy").Run(); err != nil {
		return fmt.Errorf("proxy network not found (run: docker network create proxy): %w", err)
	}
	return nil
}

func TestValidateComposeWithCLI_ValidCompose_Integration(t *testing.T) {
	dockerAvailable(t)
	validCompose := `name: test
services:
  app:
    image: nginx:alpine
`
	err := validateComposeWithCLI(validCompose)
	if err != nil {
		t.Errorf("validateComposeWithCLI(valid compose) = %v, want nil", err)
	}
}

func TestValidateComposeWithCLI_InvalidCompose_Integration(t *testing.T) {
	dockerAvailable(t)
	// Invalid: service references network not defined in top-level networks
	invalidCompose := `name: test
services:
  app:
    image: nginx
    networks:
      - undefined_network_xyz
`
	err := validateComposeWithCLI(invalidCompose)
	if err == nil {
		t.Error("validateComposeWithCLI(invalid compose) = nil, want error")
	}
	if err != nil && !strings.Contains(err.Error(), "invalid compose") {
		t.Errorf("validateComposeWithCLI error should mention 'invalid compose', got: %v", err)
	}
}

func TestValidateComposeWithCLI_InvalidYAML_Integration(t *testing.T) {
	dockerAvailable(t)
	// Invalid YAML - docker compose config will reject it
	invalidCompose := `name: test
services:
  app:
    image: [broken yaml
`
	err := validateComposeWithCLI(invalidCompose)
	if err == nil {
		t.Error("validateComposeWithCLI(invalid YAML) = nil, want error")
	}
}

func TestValidateComposeContent_ValidCompose_Integration(t *testing.T) {
	dockerAvailable(t)
	cfg := &config.Config{
		Infrastructure: config.InfrastructureConfig{
			DefaultProxyNetwork: "proxy",
		},
	}
	s := &Server{config: cfg}

	validCompose := `name: test
services:
  app:
    image: nginx:alpine
    networks:
      - proxy
networks:
  proxy:
    external: true
`
	err := s.validateComposeContent(validCompose, "test")
	if err != nil {
		t.Errorf("validateComposeContent(valid) = %v, want nil", err)
	}
}
