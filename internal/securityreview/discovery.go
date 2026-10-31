package securityreview

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/pkg/models"
	"github.com/flatrun/agent/pkg/securityprofile"
	"gopkg.in/yaml.v3"
)

type ServiceScope struct {
	Name     string                `json:"name"`
	Image    string                `json:"image"`
	Replicas int                   `json:"replicas"`
	Mounts   []models.ServiceMount `json:"mounts"`
}
type Discovery struct {
	Instances  []InstanceScope `json:"instances"`
	Deployment string          `json:"deployment"`
	Revision   string          `json:"revision"`
	Services   []ServiceScope  `json:"services"`
	Coverage   []Coverage      `json:"coverage"`
}
type InstanceScope struct {
	Service         string `json:"service"`
	ContainerID     string `json:"container_id,omitempty"`
	RunningReplicas int    `json:"running_replicas"`
	Status          string `json:"status"`
}

func Discover(d *models.Deployment) (Discovery, error) {
	discovery := Discovery{Deployment: d.Name, Services: []ServiceScope{}, Coverage: []Coverage{}}
	for _, s := range d.Services {
		discovery.Instances = append(discovery.Instances, InstanceScope{s.Name, s.ContainerID, s.RunningReplicas, s.Status})
		mounts := append([]models.ServiceMount{}, s.Mounts...)
		sort.Slice(mounts, func(i, j int) bool { return mounts[i].Target < mounts[j].Target })
		discovery.Services = append(discovery.Services, ServiceScope{s.Name, s.Image, s.Replicas, mounts})
		for _, concern := range securityprofile.Concerns {
			discovery.Coverage = append(discovery.Coverage, Coverage{Concern: concern, Service: s.Name, Status: "unknown", Reason: "Required evidence has not been collected"})
		}
	}
	sort.Slice(discovery.Services, func(i, j int) bool { return discovery.Services[i].Name < discovery.Services[j].Name })
	if len(d.Services) == 0 {
		for _, concern := range securityprofile.Concerns {
			discovery.Coverage = append(discovery.Coverage, Coverage{Concern: concern, Status: "unsupported", Reason: "Service inventory unavailable"})
		}
	}
	compose, filename, err := docker.NewDiscovery(filepath.Dir(d.Path)).GetComposeFile(filepath.Base(d.Path))
	if err != nil {
		return discovery, err
	}
	metadata, err := os.ReadFile(filepath.Join(d.Path, "service.yml"))
	if err != nil && !os.IsNotExist(err) {
		return discovery, err
	}
	configuration, err := configurationRevision(d.Path, compose)
	if err != nil {
		return discovery, err
	}
	discovery.Revision = securityprofile.Digest(struct {
		Compose     string
		Filename    string
		Environment string
		Metadata    string
		Services    []ServiceScope
	}{securityprofile.Digest(compose), filename, configuration, securityprofile.Digest(string(metadata)), discovery.Services})
	return discovery, nil
}

func DiscoveredDraft(runtime string, discovery Discovery) securityprofile.Profile {
	p := securityprofile.Draft(runtime)
	for _, s := range discovery.Services {
		roots := []string{}
		for _, m := range s.Mounts {
			if m.Target != "/" {
				roots = append(roots, m.Target)
			}
		}
		p.Targets[s.Name] = securityprofile.Target{Service: s.Name, Roots: roots, MetadataSources: []string{"inventory"}}
	}
	return p
}

func configurationRevision(root, compose string) (string, error) {
	values := map[string]string{}
	visited := map[string]bool{}
	var collect func(string, bool, int) error
	var references func(any, string, int) error
	references = func(value any, base string, depth int) error {
		switch object := value.(type) {
		case map[string]any:
			for key, value := range object {
				if key == "env_file" || key == "include" {
					entries := []any{value}
					if list, ok := value.([]any); ok {
						entries = list
					}
					for _, entry := range entries {
						path, ok := entry.(string)
						if !ok {
							if fields, exists := entry.(map[string]any); exists {
								path, ok = fields["path"].(string)
							}
						}
						if !ok {
							return fmt.Errorf("unsupported compose input reference")
						}
						if err := collect(configurationPath(base, path), key == "include", depth+1); err != nil {
							return err
						}
					}
				} else if key == "extends" {
					if fields, ok := value.(map[string]any); ok {
						if path, ok := fields["file"].(string); ok {
							if err := collect(configurationPath(base, path), true, depth+1); err != nil {
								return err
							}
						}
					}
				} else if err := references(value, base, depth); err != nil {
					return err
				}
			}
		case []any:
			for _, item := range object {
				if err := references(item, base, depth); err != nil {
					return err
				}
			}
		}
		return nil
	}
	collect = func(path string, parse bool, depth int) error {
		if depth > 16 || strings.Contains(path, "${") || strings.Contains(path, "://") {
			return fmt.Errorf("unsupported compose input boundary")
		}
		absolute := path
		if !filepath.IsAbs(absolute) {
			absolute = filepath.Join(root, absolute)
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return err
		}
		base, err := filepath.EvalSymlinks(root)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(base, resolved)
		if err != nil || rel == ".." || startsParent(rel) {
			return fmt.Errorf("external compose inputs require separate evidence support")
		}
		if visited[resolved] {
			return nil
		}
		visited[resolved] = true
		if len(values) >= 128 {
			return fmt.Errorf("compose input budget exceeded")
		}
		file, err := os.Open(resolved)
		if err != nil {
			return err
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 2<<20 {
			file.Close()
			return fmt.Errorf("unsupported compose input size or format")
		}
		data, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
		file.Close()
		if err != nil {
			return err
		}
		if len(data) > 2<<20 {
			return fmt.Errorf("compose input budget exceeded")
		}
		values[rel] = securityprofile.Digest(string(data))
		if parse {
			var object any
			if err = yaml.Unmarshal(data, &object); err != nil {
				return err
			}
			return references(object, filepath.Dir(resolved), depth)
		}
		return nil
	}
	var object any
	if err := yaml.Unmarshal([]byte(compose), &object); err != nil {
		return "", err
	}
	if err := references(object, root, 0); err != nil {
		return "", err
	}
	for _, pattern := range []string{".env*", "*compose*.yml", "*compose*.yaml", models.ComposeOverlayFile} {
		paths, _ := filepath.Glob(filepath.Join(root, pattern))
		for _, path := range paths {
			if err := collect(path, strings.HasSuffix(path, ".yml") || strings.HasSuffix(path, ".yaml"), 0); err != nil {
				return "", err
			}
		}
	}
	return securityprofile.Digest(values), nil
}

func configurationPath(base, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}
