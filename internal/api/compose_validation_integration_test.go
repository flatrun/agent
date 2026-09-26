package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/pkg/config"
)

func TestValidateComposeWithComposeGo_ValidCompose(t *testing.T) {
	validCompose := `name: test
services:
  app:
    image: nginx:alpine
`
	err := validateComposeWithComposeGo(validCompose, ".")
	if err != nil {
		t.Errorf("validateComposeWithComposeGo(valid compose) = %v, want nil", err)
	}
}

func TestValidateComposeWithComposeGo_InvalidCompose(t *testing.T) {
	invalidCompose := `name: test
services:
  app:
    image: 12345
`
	err := validateComposeWithComposeGo(invalidCompose, ".")
	if err == nil {
		t.Error("validateComposeWithComposeGo(invalid compose) = nil, want error")
	}
	if err != nil && !strings.Contains(err.Error(), "invalid compose") {
		t.Errorf("error should mention 'invalid compose', got: %v", err)
	}
}

func TestValidateComposeWithComposeGo_InvalidYAML(t *testing.T) {
	invalidCompose := `name: test
services:
  app:
    image: [broken yaml
`
	err := validateComposeWithComposeGo(invalidCompose, ".")
	if err == nil {
		t.Error("validateComposeWithComposeGo(invalid YAML) = nil, want error")
	}
}

func TestValidateComposeContent_ValidCompose_Integration(t *testing.T) {
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

// A compose with a relative env_file must validate against the deployment directory,
// where the file lives, not the agent's working directory.
func TestValidateComposeContent_RelativeEnvFile_ResolvesAgainstDeploymentDir(t *testing.T) {
	base := t.TempDir()
	const name = "envfileapp"
	deployDir := filepath.Join(base, name)
	if err := os.MkdirAll(deployDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deployDir, ".env"), []byte("FOO=bar\n"), 0644); err != nil {
		t.Fatal(err)
	}

	s := &Server{
		config: &config.Config{
			Infrastructure: config.InfrastructureConfig{DefaultProxyNetwork: "proxy"},
		},
		manager: docker.NewManager(base),
	}

	compose := `name: envfileapp
services:
  app:
    image: nginx:alpine
    env_file:
      - ./.env
    networks:
      - proxy
networks:
  proxy:
    external: true
`
	if err := s.validateComposeContent(compose, name); err != nil {
		t.Errorf("validateComposeContent with relative env_file in deployment dir = %v, want nil", err)
	}
}

func TestValidateNewComposeContent_UsesSuppliedRequiredVariable(t *testing.T) {
	s := &Server{config: &config.Config{Infrastructure: config.InfrastructureConfig{DefaultProxyNetwork: "proxy"}}}
	compose := `name: required-env
services:
  app:
    image: ${APP_IMAGE:?APP_IMAGE is required}
`
	if err := s.validateNewComposeContent(compose, "required-env", []EnvVar{{Key: "APP_IMAGE", Value: "nginx:alpine"}}, t.TempDir()); err != nil {
		t.Fatalf("validation with supplied required variable: %v", err)
	}
}

func TestValidateComposeContent_PrefersManagedEnvironment(t *testing.T) {
	base := t.TempDir()
	name := "managed-env"
	deploymentDir := filepath.Join(base, name)
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deploymentDir, ".env"), []byte("OTHER=value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deploymentDir, ".env.flatrun"), []byte("APP_IMAGE=nginx:alpine\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		config:  &config.Config{Infrastructure: config.InfrastructureConfig{DefaultProxyNetwork: "proxy"}},
		manager: docker.NewManager(base),
	}
	compose := "services:\n  app:\n    image: ${APP_IMAGE:?APP_IMAGE is required}\n"
	if err := s.validateComposeContent(compose, name); err != nil {
		t.Fatalf("validation with managed environment: %v", err)
	}
}
