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
