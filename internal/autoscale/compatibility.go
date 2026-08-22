package autoscale

import (
	"fmt"
	"sort"
	"strings"

	"github.com/flatrun/agent/pkg/models"
	"gopkg.in/yaml.v3"
)

type Compatibility struct {
	Compatible bool     `json:"compatible"`
	Service    string   `json:"service,omitempty"`
	Image      string   `json:"image,omitempty"`
	Blockers   []string `json:"blockers"`
	Warnings   []string `json:"warnings"`
	Services   []string `json:"services"`
}

type composeCompatibilityFile struct {
	Services map[string]composeCompatibilityService `yaml:"services"`
}

type composeCompatibilityService struct {
	Image       string         `yaml:"image"`
	Build       any            `yaml:"build"`
	Volumes     []any          `yaml:"volumes"`
	Configs     []any          `yaml:"configs"`
	Secrets     []any          `yaml:"secrets"`
	Devices     []any          `yaml:"devices"`
	Privileged  bool           `yaml:"privileged"`
	NetworkMode string         `yaml:"network_mode"`
	DependsOn   map[string]any `yaml:"depends_on"`
}

func AssessCompatibility(deployment *models.Deployment, composeContent string) Compatibility {
	result := Compatibility{Blockers: []string{}, Warnings: []string{}}
	if deployment == nil || deployment.Metadata == nil || deployment.Metadata.Scaling == nil {
		result.Blockers = append(result.Blockers, "Add a scaling declaration to service.yml")
		return result
	}
	scaling := deployment.Metadata.Scaling
	result.Service = strings.TrimSpace(scaling.Service)
	if result.Service == "" {
		result.Blockers = append(result.Blockers, "Choose the Compose service that may scale")
	}
	if !scaling.Stateless {
		result.Blockers = append(result.Blockers, "Only workloads declared stateless can scale across servers")
	}
	mode := strings.TrimSpace(scaling.Storage.Mode)
	if mode == "" {
		mode = "none"
	}
	if mode != "none" && mode != "shared" {
		result.Blockers = append(result.Blockers, "Storage mode must be none or shared")
	}
	if mode == "shared" && strings.TrimSpace(scaling.Storage.Class) == "" {
		result.Blockers = append(result.Blockers, "Shared storage requires a storage class")
	}

	var compose composeCompatibilityFile
	if err := yaml.Unmarshal([]byte(composeContent), &compose); err != nil {
		result.Blockers = append(result.Blockers, fmt.Sprintf("Compose configuration cannot be read: %v", err))
		return result
	}
	for name := range compose.Services {
		result.Services = append(result.Services, name)
	}
	sort.Strings(result.Services)
	service, exists := compose.Services[result.Service]
	if !exists && result.Service != "" {
		result.Blockers = append(result.Blockers, fmt.Sprintf("Compose service %q does not exist", result.Service))
		return result
	}
	result.Image = strings.TrimSpace(service.Image)
	if result.Image == "" {
		result.Blockers = append(result.Blockers, "The scale-ready service needs a portable image")
	}
	if len(service.Volumes) > 0 && mode != "shared" {
		result.Blockers = append(result.Blockers, "The service uses volumes but shared storage is not declared")
	}
	if len(service.Configs) > 0 || len(service.Secrets) > 0 {
		result.Blockers = append(result.Blockers, "Compose configs and secrets need a Fleet distribution policy")
	}
	if len(service.Devices) > 0 || service.Privileged || service.NetworkMode != "" {
		result.Blockers = append(result.Blockers, "Host-specific container access cannot move between Fleet servers")
	}
	if service.Build != nil && result.Image != "" {
		result.Warnings = append(result.Warnings, "Publish the declared image before Fleet places replicas on another server")
	}
	if len(service.DependsOn) > 0 {
		result.Warnings = append(result.Warnings, "Dependencies must be reachable from every server allowed to run this workload")
	}
	result.Compatible = len(result.Blockers) == 0
	return result
}
