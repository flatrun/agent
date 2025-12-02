package docker

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flatrun/agent/pkg/models"
	"gopkg.in/yaml.v3"
)

type Discovery struct {
	basePath string
}

func NewDiscovery(basePath string) *Discovery {
	return &Discovery{basePath: basePath}
}

type composeFile struct {
	Services map[string]composeService `yaml:"services"`
}

type composeService struct {
	Image    string        `yaml:"image"`
	Ports    []interface{} `yaml:"ports"`
	Networks []string      `yaml:"networks"`
}

func (d *Discovery) FindDeployments() ([]models.Deployment, error) {
	return d.findDeploymentsWithFilter(false)
}

func (d *Discovery) FindInfrastructure() ([]models.Deployment, error) {
	return d.findDeploymentsWithFilter(true)
}

func (d *Discovery) findDeploymentsWithFilter(infraOnly bool) ([]models.Deployment, error) {
	var deployments []models.Deployment

	entries, err := os.ReadDir(d.basePath)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		dirPath := filepath.Join(d.basePath, entry.Name())
		composePath := d.findComposeFile(dirPath)

		if composePath == "" {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		deployment := models.Deployment{
			Name:      entry.Name(),
			Path:      dirPath,
			Status:    string(models.StatusUnknown),
			CreatedAt: info.ModTime(),
			UpdatedAt: info.ModTime(),
		}

		metadataPath := filepath.Join(dirPath, "service.yml")
		if metadata, err := d.loadMetadata(metadataPath); err == nil {
			deployment.Metadata = metadata
		}

		isInfra := deployment.Metadata != nil && deployment.Metadata.Type == "infrastructure"
		if infraOnly && !isInfra {
			continue
		}
		if !infraOnly && isInfra {
			continue
		}

		if services, err := d.parseComposeServices(composePath); err == nil {
			deployment.Services = services
		}

		deployments = append(deployments, deployment)
	}

	return deployments, nil
}

func (d *Discovery) GetDeployment(name string) (*models.Deployment, error) {
	dirPath := filepath.Join(d.basePath, name)

	info, err := os.Stat(dirPath)
	if err != nil {
		return nil, err
	}

	if !info.IsDir() {
		return nil, os.ErrNotExist
	}

	composePath := d.findComposeFile(dirPath)
	if composePath == "" {
		return nil, os.ErrNotExist
	}

	deployment := &models.Deployment{
		Name:      name,
		Path:      dirPath,
		Status:    string(models.StatusUnknown),
		CreatedAt: info.ModTime(),
		UpdatedAt: info.ModTime(),
	}

	metadataPath := filepath.Join(dirPath, "service.yml")
	metadata, err := d.loadMetadata(metadataPath)
	if err != nil {
		metadata = d.generateMetadataFromCompose(composePath, name)
		if metadata != nil {
			_ = d.SaveMetadata(name, metadata)
		}
	}
	deployment.Metadata = metadata

	if services, err := d.parseComposeServices(composePath); err == nil {
		deployment.Services = services
	}

	return deployment, nil
}

func (d *Discovery) findComposeFile(dirPath string) string {
	candidates := []string{
		"docker-compose.yml",
		"docker-compose.yaml",
		"compose.yml",
		"compose.yaml",
	}

	for _, candidate := range candidates {
		path := filepath.Join(dirPath, candidate)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	return ""
}

func (d *Discovery) parseComposeServices(composePath string) ([]models.Service, error) {
	data, err := os.ReadFile(composePath)
	if err != nil {
		return nil, err
	}

	var compose composeFile
	if err := yaml.Unmarshal(data, &compose); err != nil {
		return nil, err
	}

	var services []models.Service
	for name, svc := range compose.Services {
		service := models.Service{
			Name:     name,
			Image:    svc.Image,
			Status:   "unknown",
			Networks: svc.Networks,
		}

		for _, p := range svc.Ports {
			portStr := d.parsePort(p)
			if portStr != "" {
				service.Ports = append(service.Ports, portStr)
			}
		}

		services = append(services, service)
	}

	return services, nil
}

func (d *Discovery) parsePort(port interface{}) string {
	switch v := port.(type) {
	case string:
		return v
	case int:
		return fmt.Sprintf("%d", v)
	case map[string]interface{}:
		target, hasTarget := v["target"]
		published, hasPublished := v["published"]
		if hasTarget && hasPublished {
			return fmt.Sprintf("%v:%v", published, target)
		} else if hasTarget {
			return fmt.Sprintf("%v", target)
		}
	}
	return ""
}

func (d *Discovery) loadMetadata(path string) (*models.ServiceMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var metadata models.ServiceMetadata
	if err := yaml.Unmarshal(data, &metadata); err != nil {
		return nil, err
	}

	return &metadata, nil
}

func (d *Discovery) CreateDeployment(name string, composeContent string) error {
	dirPath := filepath.Join(d.basePath, name)

	if err := os.MkdirAll(dirPath, 0755); err != nil {
		return err
	}

	// Ensure compose file has a name attribute for project identification
	composeContent = d.ensureComposeName(name, composeContent)

	composePath := filepath.Join(dirPath, "docker-compose.yml")
	return os.WriteFile(composePath, []byte(composeContent), 0644)
}

// ensureComposeName adds or updates the name attribute in a compose file
func (d *Discovery) ensureComposeName(name string, content string) string {
	var compose map[string]interface{}
	if err := yaml.Unmarshal([]byte(content), &compose); err != nil {
		// If parsing fails, prepend name manually
		return fmt.Sprintf("name: %s\n%s", name, content)
	}

	// Check if name already exists
	if _, exists := compose["name"]; exists {
		return content
	}

	// Add name attribute
	compose["name"] = name

	// Re-marshal with name included
	data, err := yaml.Marshal(compose)
	if err != nil {
		return fmt.Sprintf("name: %s\n%s", name, content)
	}

	return string(data)
}

func (d *Discovery) DeleteDeployment(name string) error {
	dirPath := filepath.Join(d.basePath, name)
	return os.RemoveAll(dirPath)
}

func (d *Discovery) GetComposeFile(name string) (string, error) {
	dirPath := filepath.Join(d.basePath, name)
	composePath := d.findComposeFile(dirPath)

	if composePath == "" {
		return "", os.ErrNotExist
	}

	data, err := os.ReadFile(composePath)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

func (d *Discovery) UpdateComposeFile(name string, content string) error {
	dirPath := filepath.Join(d.basePath, name)
	composePath := d.findComposeFile(dirPath)

	if composePath == "" {
		composePath = filepath.Join(dirPath, "docker-compose.yml")
	}

	backup := composePath + ".bak." + time.Now().Format("20060102150405")
	if data, err := os.ReadFile(composePath); err == nil {
		_ = os.WriteFile(backup, data, 0644)
	}

	return os.WriteFile(composePath, []byte(content), 0644)
}

func (d *Discovery) SaveMetadata(name string, metadata *models.ServiceMetadata) error {
	dirPath := filepath.Join(d.basePath, name)
	metadataPath := filepath.Join(dirPath, "service.yml")

	data, err := yaml.Marshal(metadata)
	if err != nil {
		return err
	}

	return os.WriteFile(metadataPath, data, 0644)
}

func (d *Discovery) DeleteMetadata(name string) error {
	dirPath := filepath.Join(d.basePath, name)
	metadataPath := filepath.Join(dirPath, "service.yml")

	if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
		return nil
	}

	return os.Remove(metadataPath)
}

func (d *Discovery) generateMetadataFromCompose(composePath, name string) *models.ServiceMetadata {
	data, err := os.ReadFile(composePath)
	if err != nil {
		return nil
	}

	var compose composeFile
	if err := yaml.Unmarshal(data, &compose); err != nil {
		return nil
	}

	metadata := &models.ServiceMetadata{
		Name: name,
		Type: "",
		Networking: models.NetworkingConfig{
			Expose:    false,
			Protocol:  "http",
			ProxyType: "http",
		},
		SSL: models.SSLConfig{
			Enabled:  false,
			AutoCert: false,
		},
		HealthCheck: models.HealthCheckConfig{
			Path:     "/",
			Interval: "30s",
		},
	}

	for _, svc := range compose.Services {
		if len(svc.Ports) > 0 {
			portStr := d.parsePort(svc.Ports[0])
			if portStr != "" {
				port := d.extractContainerPort(portStr)
				if port > 0 {
					metadata.Networking.ContainerPort = port
				}
			}
			break
		}
	}

	return metadata
}

func (d *Discovery) extractContainerPort(portStr string) int {
	parts := strings.Split(portStr, ":")
	var portPart string
	if len(parts) == 2 {
		portPart = parts[1]
	} else if len(parts) == 1 {
		portPart = parts[0]
	} else {
		return 0
	}

	portPart = strings.Split(portPart, "/")[0]

	port, err := strconv.Atoi(portPart)
	if err != nil {
		return 0
	}
	return port
}
