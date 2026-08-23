package autoscale

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/docker/go-units"
	"github.com/flatrun/agent/internal/orchestrator"
	"github.com/flatrun/agent/pkg/models"
	"gopkg.in/yaml.v3"
)

type Compatibility struct {
	Compatible bool                  `json:"compatible"`
	Service    string                `json:"service,omitempty"`
	Image      string                `json:"image,omitempty"`
	Blockers   []string              `json:"blockers"`
	Warnings   []string              `json:"warnings"`
	Services   []string              `json:"services"`
	Workload   *models.ScalingConfig `json:"workload,omitempty"`
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
	EnvFile     any            `yaml:"env_file"`
	Environment any            `yaml:"environment"`
	Entrypoint  any            `yaml:"entrypoint"`
	Command     any            `yaml:"command"`
	WorkingDir  string         `yaml:"working_dir"`
	Deploy      composeDeploy  `yaml:"deploy"`
}

type composeDeploy struct {
	Resources composeResources `yaml:"resources"`
}

type composeResources struct {
	Limits       composeResourceValues `yaml:"limits"`
	Reservations composeResourceValues `yaml:"reservations"`
}

type composeResourceValues struct {
	CPUs   any `yaml:"cpus"`
	Memory any `yaml:"memory"`
}

func AssessCompatibility(deployment *models.Deployment, composeContent string) Compatibility {
	result := Compatibility{Blockers: []string{}, Warnings: []string{}}
	if deployment == nil || deployment.Metadata == nil || deployment.Metadata.Scaling == nil {
		result.Blockers = append(result.Blockers, "Add a scaling declaration to service.yml")
		return result
	}
	scaling := deployment.Metadata.Scaling
	result.Workload = scaling
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
	if mode == "shared" {
		result.Blockers = append(result.Blockers, "Shared storage needs an installed Fleet storage adapter")
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
	if service.EnvFile != nil {
		result.Blockers = append(result.Blockers, "Environment files must be converted to inline deployment environment values")
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

func BuildWorkload(deployment *models.Deployment, composeContent string, replicas int, proxyNetwork string) (orchestrator.Workload, error) {
	compatibility := AssessCompatibility(deployment, composeContent)
	if !compatibility.Compatible {
		return orchestrator.Workload{}, fmt.Errorf("workload is not scale-ready: %s", strings.Join(compatibility.Blockers, "; "))
	}
	var compose composeCompatibilityFile
	if err := yaml.Unmarshal([]byte(composeContent), &compose); err != nil {
		return orchestrator.Workload{}, err
	}
	service := compose.Services[compatibility.Service]
	workload := orchestrator.Workload{
		ID: deployment.Name, Image: service.Image, Replicas: replicas,
		Environment: map[string]string{}, Entrypoint: stringList(service.Entrypoint), Command: stringList(service.Command),
		WorkingDir: service.WorkingDir, Labels: map[string]string{"flatrun.deployment": deployment.Name},
	}
	parsedResources, err := workloadResources(service.Deploy.Resources)
	if err != nil {
		return orchestrator.Workload{}, err
	}
	workload.Resources = parsedResources
	if strings.TrimSpace(proxyNetwork) != "" {
		workload.Networks = []string{proxyNetwork}
	}
	workload.Environment = environmentMap(service.Environment)
	for _, domain := range deployment.Metadata.GetDomains() {
		if domain.Service == compatibility.Service {
			workload.Port = domain.ContainerPort
			break
		}
	}
	if checkType := strings.ToLower(strings.TrimSpace(deployment.Metadata.HealthCheck.Type)); checkType == "" || checkType == "http" {
		workload.Health.Path = deployment.Metadata.HealthCheck.Path
	}
	return workload, nil
}

func workloadResources(resources composeResources) (orchestrator.Resources, error) {
	cpuLimit, err := cpuValue(resources.Limits.CPUs)
	if err != nil {
		return orchestrator.Resources{}, fmt.Errorf("invalid CPU limit: %w", err)
	}
	cpuRequest, err := cpuValue(resources.Reservations.CPUs)
	if err != nil {
		return orchestrator.Resources{}, fmt.Errorf("invalid CPU reservation: %w", err)
	}
	memoryLimit, err := memoryValue(resources.Limits.Memory)
	if err != nil {
		return orchestrator.Resources{}, fmt.Errorf("invalid memory limit: %w", err)
	}
	memoryRequest, err := memoryValue(resources.Reservations.Memory)
	if err != nil {
		return orchestrator.Resources{}, fmt.Errorf("invalid memory reservation: %w", err)
	}
	return orchestrator.Resources{CPURequest: cpuRequest, CPULimit: cpuLimit, MemoryRequest: memoryRequest, MemoryLimit: memoryLimit}, nil
}

func cpuValue(value any) (float64, error) {
	if value == nil {
		return 0, nil
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(fmt.Sprint(value)), 64)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("%q", value)
	}
	return parsed, nil
}

func memoryValue(value any) (uint64, error) {
	if value == nil {
		return 0, nil
	}
	parsed, err := units.RAMInBytes(strings.TrimSpace(fmt.Sprint(value)))
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("%q", value)
	}
	return uint64(parsed), nil
}

func environmentMap(value any) map[string]string {
	result := map[string]string{}
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if item != nil {
				result[key] = fmt.Sprint(item)
			}
		}
	case []any:
		for _, item := range typed {
			parts := strings.SplitN(fmt.Sprint(item), "=", 2)
			if len(parts) == 2 {
				result[parts[0]] = parts[1]
			}
		}
	}
	return result
}

func stringList(value any) []string {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []string{"/bin/sh", "-c", typed}
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			result = append(result, fmt.Sprint(item))
		}
		return result
	default:
		return nil
	}
}
