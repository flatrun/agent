package api

import (
	"strings"
	"testing"

	"github.com/flatrun/agent/pkg/config"
)

func TestValidateComposeWithComposeGo_ValidCompose(t *testing.T) {
	validCompose := `name: test
services:
  app:
    image: nginx:alpine
`
	err := validateComposeWithComposeGo(validCompose)
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
	err := validateComposeWithComposeGo(invalidCompose)
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
	err := validateComposeWithComposeGo(invalidCompose)
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
