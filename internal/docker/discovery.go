package docker

import (
	"os"
	"path/filepath"
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

func (d *Discovery) FindDeployments() ([]models.Deployment, error) {
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
	if metadata, err := d.loadMetadata(metadataPath); err == nil {
		deployment.Metadata = metadata
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

	composePath := filepath.Join(dirPath, "docker-compose.yml")
	return os.WriteFile(composePath, []byte(composeContent), 0644)
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
		os.WriteFile(backup, data, 0644)
	}

	return os.WriteFile(composePath, []byte(content), 0644)
}
