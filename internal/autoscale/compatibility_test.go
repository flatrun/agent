package autoscale

import (
	"testing"

	"github.com/flatrun/agent/pkg/models"
)

func TestAssessCompatibilityAcceptsDeclaredStatelessImage(t *testing.T) {
	deployment := &models.Deployment{Metadata: &models.ServiceMetadata{Scaling: &models.ScalingConfig{Service: "web", Stateless: true}}}
	result := AssessCompatibility(deployment, "services:\n  web:\n    image: registry.example.com/shop:1\n")
	if !result.Compatible || result.Service != "web" || result.Image != "registry.example.com/shop:1" {
		t.Fatalf("result = %#v", result)
	}
}

func TestAssessCompatibilityExplainsUnsafeComposeFeatures(t *testing.T) {
	deployment := &models.Deployment{Metadata: &models.ServiceMetadata{Scaling: &models.ScalingConfig{Service: "web", Stateless: true}}}
	result := AssessCompatibility(deployment, "services:\n  web:\n    image: shop:1\n    privileged: true\n    volumes:\n      - ./data:/data\n")
	if result.Compatible || len(result.Blockers) != 2 {
		t.Fatalf("result = %#v", result)
	}
}

func TestBuildWorkloadCarriesPortableRuntimeInputs(t *testing.T) {
	deployment := &models.Deployment{Name: "shop", Metadata: &models.ServiceMetadata{
		Scaling:     &models.ScalingConfig{Service: "web", Stateless: true},
		Domains:     []models.DomainConfig{{Service: "web", ContainerPort: 8080, Domain: "shop.example.com"}},
		HealthCheck: models.HealthCheckConfig{Path: "/ready"},
	}}
	workload, err := BuildWorkload(deployment, `services:
  web:
    image: registry.example.com/shop:1
    environment:
      APP_ENV: production
    entrypoint: ["/app/entrypoint"]
    command: ["serve", "--port", "8080"]
    working_dir: /app
`, 2, "proxy")
	if err != nil {
		t.Fatal(err)
	}
	if workload.Replicas != 2 || workload.Port != 8080 || workload.Environment["APP_ENV"] != "production" || workload.WorkingDir != "/app" || workload.Health.Path != "/ready" {
		t.Fatalf("workload = %#v", workload)
	}
	if len(workload.Entrypoint) != 1 || len(workload.Command) != 3 {
		t.Fatalf("workload commands = %#v / %#v", workload.Entrypoint, workload.Command)
	}
	if len(workload.Networks) != 1 || workload.Networks[0] != "proxy" {
		t.Fatalf("workload networks = %#v", workload.Networks)
	}
}
