package docker

import (
	"fmt"
	"os"
	"os/exec"
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
	Expose   []interface{} `yaml:"expose"`
	Networks []string      `yaml:"networks"`
	Volumes  []string      `yaml:"volumes"`
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
	return FindComposeFile(dirPath)
}

// FindComposeFile returns the path to the compose file in dirPath, preferring the
// standard filenames and falling back to a *compose*.yml/yaml glob. Returns "" when none.
func FindComposeFile(dirPath string) string {
	// First check exact standard names (preferred)
	standardNames := []string{
		"docker-compose.yml",
		"docker-compose.yaml",
		"compose.yml",
		"compose.yaml",
	}

	for _, name := range standardNames {
		path := filepath.Join(dirPath, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	// Fallback to pattern matching for *compose*.yml/yaml files
	patterns := []string{
		"*compose*.yml",
		"*compose*.yaml",
	}

	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(dirPath, pattern))
		if err == nil && len(matches) > 0 {
			return matches[0]
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
		for _, p := range svc.Expose {
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

func (d *Discovery) CreateDeployment(name string, composeContent string, fileMounts []string) error {
	dirPath := filepath.Join(d.basePath, name)

	if err := os.MkdirAll(dirPath, 0755); err != nil {
		return err
	}

	// Ensure compose file has a name attribute for project identification
	composeContent = d.ensureComposeName(name, composeContent)

	// Pre-create bind mount directories with permissive access for non-root containers
	if err := d.createBindMountDirs(dirPath, composeContent, fileMounts); err != nil {
		return fmt.Errorf("failed to create mount directories: %w", err)
	}

	composePath := filepath.Join(dirPath, "docker-compose.yml")
	return os.WriteFile(composePath, []byte(composeContent), 0644)
}

// createBindMountDirs parses compose content and creates bind mount directories
// with world-writable permissions to support non-root containers (e.g., Bitnami).
// fileMounts lists relative paths (e.g., "./nginx.conf") that are file mounts
// from template metadata. For paths not in fileMounts, a basename-contains-dot
// heuristic is used as a fallback to avoid creating files as directories.
func (d *Discovery) createBindMountDirs(deploymentPath, composeContent string, fileMounts []string) error {
	var compose struct {
		Services map[string]struct {
			Volumes []string `yaml:"volumes"`
		} `yaml:"services"`
	}

	if err := yaml.Unmarshal([]byte(composeContent), &compose); err != nil {
		return nil // Skip if parse fails, not critical
	}

	fileMountSet := make(map[string]bool, len(fileMounts))
	for _, fm := range fileMounts {
		cleanPath := filepath.Clean(fm)
		fileMountSet[cleanPath] = true
		fileMountSet["./"+cleanPath] = true
	}

	for _, service := range compose.Services {
		for _, volume := range service.Volumes {
			hostPath := extractBindMountPath(volume)
			if hostPath == "" {
				continue
			}

			if !strings.HasPrefix(hostPath, "./") && !strings.HasPrefix(hostPath, "../") {
				continue
			}

			fullPath := filepath.Join(deploymentPath, hostPath)

			if _, err := os.Stat(fullPath); err == nil {
				continue
			}

			if isFileMount(hostPath, fileMountSet) {
				parentDir := filepath.Dir(fullPath)
				if err := os.MkdirAll(parentDir, 0777); err != nil {
					return err
				}
				if err := os.Chmod(parentDir, 0777); err != nil {
					return err
				}
				continue
			}

			if err := os.MkdirAll(fullPath, 0777); err != nil {
				return err
			}
			if err := os.Chmod(fullPath, 0777); err != nil {
				return err
			}
		}
	}

	return nil
}

// isFileMount checks whether a bind mount host path refers to a file.
// It first checks the metadata-provided fileMounts set, then falls back
// to a heuristic: if the basename contains a dot and is not a known
// directory pattern (e.g., conf.d), it's treated as a file.
func isFileMount(hostPath string, fileMountSet map[string]bool) bool {
	if fileMountSet[hostPath] {
		return true
	}

	base := filepath.Base(hostPath)
	if !strings.Contains(base, ".") {
		return false
	}

	// Known directory suffixes like .d (conf.d, certs.d, etc.)
	if strings.HasSuffix(base, ".d") {
		return false
	}

	return true
}

// extractBindMountPath extracts the host path from a volume mount string
// Handles formats: "./path:/container/path" or "./path:/container/path:ro"
func extractBindMountPath(volume string) string {
	// Skip named volumes (no colon or starts with volume name)
	if !strings.Contains(volume, ":") {
		return ""
	}

	parts := strings.SplitN(volume, ":", 2)
	if len(parts) < 2 {
		return ""
	}

	hostPath := parts[0]

	// Skip named volumes (don't start with . or /)
	if !strings.HasPrefix(hostPath, ".") && !strings.HasPrefix(hostPath, "/") {
		return ""
	}

	return hostPath
}

// MountOwnership describes ownership and subdirectory requirements for a bind mount.
type MountOwnership struct {
	HostPath       string
	User           string // "UID:GID" or empty
	Subdirectories []string
}

// ApplyMountOwnership sets ownership and creates subdirectories for bind mounts.
// When User is specified (UID:GID format), the mount is chowned recursively to
// that user so intermediate directories and pre-existing content end up owned
// by the container too. When User is empty, directories are chmod'd to 0777 as
// a fallback for non-template deploys. A host path that already exists as a
// regular file (e.g. a generated .env) is only chowned, never turned into a
// directory.
func (d *Discovery) ApplyMountOwnership(deploymentPath string, mounts []MountOwnership) error {
	for _, m := range mounts {
		base := m.HostPath
		if !filepath.IsAbs(base) {
			base = filepath.Join(deploymentPath, base)
		}

		var uid, gid int
		if m.User != "" {
			var err error
			uid, gid, err = parseUIDGID(m.User)
			if err != nil {
				return fmt.Errorf("parse user %q: %w", m.User, err)
			}
		}

		if info, err := os.Lstat(base); err == nil && !info.IsDir() {
			if m.User != "" {
				if err := os.Lchown(base, uid, gid); err != nil {
					return fmt.Errorf("chown %s: %w", base, err)
				}
			}
			continue
		}

		if err := os.MkdirAll(base, 0755); err != nil {
			return fmt.Errorf("create mount dir %s: %w", base, err)
		}

		dirs := []string{base}
		for _, sub := range m.Subdirectories {
			subPath := filepath.Join(base, sub)
			if err := os.MkdirAll(subPath, 0755); err != nil {
				return fmt.Errorf("create subdirectory %s: %w", subPath, err)
			}
			dirs = append(dirs, subPath)
		}

		if m.User != "" {
			err := filepath.WalkDir(base, func(path string, _ os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				// Runs as root over container-written content: following a
				// planted symlink would chown an arbitrary host file.
				return os.Lchown(path, uid, gid)
			})
			if err != nil {
				return fmt.Errorf("chown %s: %w", base, err)
			}
		} else {
			for _, dir := range dirs {
				if err := os.Chmod(dir, 0777); err != nil {
					return fmt.Errorf("chmod %s: %w", dir, err)
				}
			}
		}
	}
	return nil
}

func parseUIDGID(user string) (int, int, error) {
	parts := strings.SplitN(user, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected UID:GID format, got %q", user)
	}
	uid, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid UID %q: %w", parts[0], err)
	}
	gid, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid GID %q: %w", parts[1], err)
	}
	return uid, gid, nil
}

// InspectContainerUser gets the UID:GID of the running process inside a container.
func InspectContainerUser(containerName string) (string, error) {
	uidCmd := exec.Command("docker", "exec", containerName, "id", "-u")
	uidOut, err := uidCmd.Output()
	if err != nil {
		return "", fmt.Errorf("get container uid: %w", err)
	}

	gidCmd := exec.Command("docker", "exec", containerName, "id", "-g")
	gidOut, err := gidCmd.Output()
	if err != nil {
		return "", fmt.Errorf("get container gid: %w", err)
	}

	uid := strings.TrimSpace(string(uidOut))
	gid := strings.TrimSpace(string(gidOut))

	return uid + ":" + gid, nil
}

// ExtractBindMounts parses compose content and returns bind mount host paths.
func ExtractBindMounts(composeContent string) []string {
	var compose composeFile
	if err := yaml.Unmarshal([]byte(composeContent), &compose); err != nil {
		return nil
	}

	var paths []string
	seen := make(map[string]bool)

	for _, service := range compose.Services {
		for _, volume := range service.Volumes {
			hostPath := extractBindMountPath(volume)
			if hostPath == "" {
				continue
			}
			if !strings.HasPrefix(hostPath, "./") && !strings.HasPrefix(hostPath, "../") {
				continue
			}
			if !seen[hostPath] {
				seen[hostPath] = true
				paths = append(paths, hostPath)
			}
		}
	}

	return paths
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

func (d *Discovery) GetComposeFile(name string) (string, string, error) {
	dirPath := filepath.Join(d.basePath, name)
	composePath := d.findComposeFile(dirPath)

	if composePath == "" {
		return "", "", os.ErrNotExist
	}

	data, err := os.ReadFile(composePath)
	if err != nil {
		return "", "", err
	}

	filename := filepath.Base(composePath)
	return string(data), filename, nil
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

	if err := os.WriteFile(composePath, []byte(content), 0644); err != nil {
		return err
	}

	metadataPath := filepath.Join(dirPath, "service.yml")
	if _, err := os.Stat(metadataPath); err == nil {
		if newMeta := d.generateMetadataFromCompose(composePath, name); newMeta != nil {
			existing, err := d.loadMetadata(metadataPath)
			if err == nil {
				existing.Networking.ContainerPort = newMeta.Networking.ContainerPort
				// Respect a user-pinned primary service; only let auto-detection set the
				// routing service when the user has not explicitly chosen one.
				if existing.PrimaryService != "" {
					existing.Networking.Service = existing.PrimaryService
				} else if newMeta.Networking.Service != "" {
					existing.Networking.Service = newMeta.Networking.Service
				}
				if err := d.SaveMetadata(name, existing); err != nil {
					return fmt.Errorf("failed to sync service metadata: %w", err)
				}
			}
		}
	}

	return nil
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

	svcName, svc := d.pickPrimaryService(compose.Services)
	if svcName != "" {
		metadata.Networking.Service = svcName
		if len(svc.Ports) > 0 {
			if portStr := d.parsePort(svc.Ports[0]); portStr != "" {
				if port := d.extractContainerPort(portStr); port > 0 {
					metadata.Networking.ContainerPort = port
				}
			}
		} else if len(svc.Expose) > 0 {
			if portStr := d.parsePort(svc.Expose[0]); portStr != "" {
				if port := d.extractContainerPort(portStr); port > 0 {
					metadata.Networking.ContainerPort = port
				}
			}
		}
	}

	return metadata
}

// pickPrimaryService selects the service to use for networking metadata.
// For single-service composes, returns that service. For multi-service,
// it prefers a service named "app" or "web" that exposes ports, then falls
// back to the first service with exposed ports. The preference keeps the
// selection deterministic, since Go map iteration order is random.
func (d *Discovery) pickPrimaryService(services map[string]composeService) (string, composeService) {
	if len(services) == 1 {
		for name, svc := range services {
			return name, svc
		}
	}
	for name, svc := range services {
		if (name == "app" || name == "web") && (len(svc.Ports) > 0 || len(svc.Expose) > 0) {
			return name, svc
		}
	}
	for name, svc := range services {
		if len(svc.Ports) > 0 || len(svc.Expose) > 0 {
			return name, svc
		}
	}
	return "", composeService{}
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
