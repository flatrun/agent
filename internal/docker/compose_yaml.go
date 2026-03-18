package docker

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

var topLevelKeyOrder = []string{
	"name",
	"version",
	"services",
	"networks",
	"volumes",
	"configs",
	"secrets",
}

var serviceKeyOrder = []string{
	"image",
	"build",
	"container_name",
	"command",
	"entrypoint",
	"working_dir",
	"user",
	"environment",
	"env_file",
	"ports",
	"expose",
	"volumes",
	"networks",
	"depends_on",
	"links",
	"healthcheck",
	"restart",
	"deploy",
	"labels",
	"logging",
	"extra_hosts",
	"dns",
	"cap_add",
	"cap_drop",
	"privileged",
	"security_opt",
	"sysctls",
	"ulimits",
	"devices",
	"tmpfs",
	"tty",
	"stdin_open",
	"stop_signal",
	"stop_grace_period",
	"platform",
	"pull_policy",
	"profiles",
	"extends",
}

func MarshalComposeYAML(compose map[string]interface{}) ([]byte, error) {
	root := &yaml.Node{Kind: yaml.DocumentNode}
	content := &yaml.Node{Kind: yaml.MappingNode}
	root.Content = append(root.Content, content)

	for _, key := range topLevelKeyOrder {
		if val, exists := compose[key]; exists {
			if key == "services" {
				if services, ok := val.(map[string]interface{}); ok {
					addServicesNode(content, services)
					continue
				}
			}
			addKeyValue(content, key, val)
		}
	}

	for key, val := range compose {
		if !contains(topLevelKeyOrder, key) {
			addKeyValue(content, key, val)
		}
	}

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(root); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func addServicesNode(parent *yaml.Node, services map[string]interface{}) {
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: "services"}
	valueNode := &yaml.Node{Kind: yaml.MappingNode}

	var serviceNames []string
	for name := range services {
		serviceNames = append(serviceNames, name)
	}

	for _, name := range serviceNames {
		serviceData := services[name]
		service, ok := serviceData.(map[string]interface{})
		if !ok {
			addKeyValue(valueNode, name, serviceData)
			continue
		}

		serviceKeyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: name}
		serviceValueNode := &yaml.Node{Kind: yaml.MappingNode}

		for _, key := range serviceKeyOrder {
			if val, exists := service[key]; exists {
				addKeyValue(serviceValueNode, key, val)
			}
		}

		for key, val := range service {
			if !contains(serviceKeyOrder, key) {
				addKeyValue(serviceValueNode, key, val)
			}
		}

		valueNode.Content = append(valueNode.Content, serviceKeyNode, serviceValueNode)
	}

	parent.Content = append(parent.Content, keyNode, valueNode)
}

func addKeyValue(parent *yaml.Node, key string, value interface{}) {
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: key}

	var valueNode yaml.Node
	if err := valueNode.Encode(value); err != nil {
		return
	}

	parent.Content = append(parent.Content, keyNode, &valueNode)
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func ParseComposeYAML(content string) (map[string]interface{}, error) {
	var compose map[string]interface{}
	if err := yaml.Unmarshal([]byte(content), &compose); err != nil {
		return nil, err
	}
	return compose, nil
}

// EnsureContainerNames ensures all services have explicit container_name set.
// The primary service (preferring "app") gets deploymentName as its container name.
// All other services get "{deploymentName}-{serviceName}".
// Existing container_name values are preserved.
func EnsureContainerNames(content string, deploymentName string) (string, error) {
	if deploymentName == "" {
		return content, nil
	}

	compose, err := ParseComposeYAML(content)
	if err != nil {
		return content, err
	}

	services, ok := compose["services"].(map[string]interface{})
	if !ok || len(services) == 0 {
		return content, nil
	}

	// Find the primary service (prefer "app" if exists, otherwise take first)
	var primaryService string
	for name := range services {
		if name == "app" {
			primaryService = "app"
			break
		}
		if primaryService == "" {
			primaryService = name
		}
	}

	for name := range services {
		service, ok := services[name].(map[string]interface{})
		if !ok {
			continue
		}

		if _, hasContainerName := service["container_name"]; hasContainerName {
			continue
		}

		if name == primaryService {
			service["container_name"] = deploymentName
		} else {
			service["container_name"] = fmt.Sprintf("%s-%s", deploymentName, name)
		}
		services[name] = service
	}

	result, err := MarshalComposeYAML(compose)
	if err != nil {
		return content, err
	}

	return strings.TrimSpace(string(result)), nil
}

// HasVolumeMount checks if a service has a specific volume mount
func HasVolumeMount(content string, serviceName string, volumeMount string) bool {
	compose, err := ParseComposeYAML(content)
	if err != nil {
		return false
	}

	services, ok := compose["services"].(map[string]interface{})
	if !ok {
		return false
	}

	service, ok := services[serviceName].(map[string]interface{})
	if !ok {
		return false
	}

	switch v := service["volumes"].(type) {
	case []interface{}:
		for _, vol := range v {
			if volStr, ok := vol.(string); ok {
				if volStr == volumeMount {
					return true
				}
			}
		}
	}

	return false
}

// AddVolumeToService adds a volume mount to a specific service in the compose file
func AddVolumeToService(content string, serviceName string, volumeMount string) (string, error) {
	if HasVolumeMount(content, serviceName, volumeMount) {
		return content, nil
	}

	compose, err := ParseComposeYAML(content)
	if err != nil {
		return content, err
	}

	services, ok := compose["services"].(map[string]interface{})
	if !ok {
		return content, nil
	}

	service, ok := services[serviceName].(map[string]interface{})
	if !ok {
		return content, nil
	}

	var volumes []string
	if v, ok := service["volumes"].([]interface{}); ok {
		for _, vol := range v {
			if volStr, ok := vol.(string); ok {
				volumes = append(volumes, volStr)
			}
		}
	}

	volumes = append(volumes, volumeMount)
	volumesList := make([]interface{}, len(volumes))
	for i, v := range volumes {
		volumesList[i] = v
	}
	service["volumes"] = volumesList
	services[serviceName] = service

	result, err := MarshalComposeYAML(compose)
	if err != nil {
		return content, err
	}

	return strings.TrimSpace(string(result)), nil
}

// RemoveVolumeFromService removes a volume mount from a specific service in the compose file
func RemoveVolumeFromService(content string, serviceName string, volumeMount string) (string, error) {
	if !HasVolumeMount(content, serviceName, volumeMount) {
		return content, nil
	}

	compose, err := ParseComposeYAML(content)
	if err != nil {
		return content, err
	}

	services, ok := compose["services"].(map[string]interface{})
	if !ok {
		return content, nil
	}

	service, ok := services[serviceName].(map[string]interface{})
	if !ok {
		return content, nil
	}

	var volumes []string
	if v, ok := service["volumes"].([]interface{}); ok {
		for _, vol := range v {
			if volStr, ok := vol.(string); ok {
				if volStr != volumeMount {
					volumes = append(volumes, volStr)
				}
			}
		}
	}

	if len(volumes) == 0 {
		delete(service, "volumes")
	} else {
		volumesList := make([]interface{}, len(volumes))
		for i, v := range volumes {
			volumesList[i] = v
		}
		service["volumes"] = volumesList
	}
	services[serviceName] = service

	result, err := MarshalComposeYAML(compose)
	if err != nil {
		return content, err
	}

	return strings.TrimSpace(string(result)), nil
}

func AddNetworkToCompose(content string, networkName string) (string, error) {
	if networkName == "" {
		return content, nil
	}

	compose, err := ParseComposeYAML(content)
	if err != nil {
		return content, err
	}

	networks, ok := compose["networks"].(map[string]interface{})
	if !ok {
		networks = make(map[string]interface{})
		compose["networks"] = networks
	}

	referencedNetworks := make(map[string]bool)

	services, ok := compose["services"].(map[string]interface{})
	if ok {
		for serviceName, serviceData := range services {
			service, ok := serviceData.(map[string]interface{})
			if !ok {
				continue
			}

			var serviceNetworks []string
			switch n := service["networks"].(type) {
			case []interface{}:
				for _, net := range n {
					if netStr, ok := net.(string); ok {
						serviceNetworks = append(serviceNetworks, netStr)
						referencedNetworks[netStr] = true
					}
				}
			case map[string]interface{}:
				for netName := range n {
					serviceNetworks = append(serviceNetworks, netName)
					referencedNetworks[netName] = true
				}
			}

			hasNetwork := false
			for _, net := range serviceNetworks {
				if net == networkName {
					hasNetwork = true
					break
				}
			}

			if !hasNetwork {
				serviceNetworks = append(serviceNetworks, networkName)
				networksList := make([]interface{}, len(serviceNetworks))
				for i, n := range serviceNetworks {
					networksList[i] = n
				}
				service["networks"] = networksList
				services[serviceName] = service
			}
		}
	}

	if _, exists := networks[networkName]; !exists {
		networks[networkName] = map[string]interface{}{
			"external": true,
		}
	}

	for netName := range referencedNetworks {
		if _, exists := networks[netName]; !exists {
			networks[netName] = map[string]interface{}{}
		}
	}

	result, err := MarshalComposeYAML(compose)
	if err != nil {
		return content, err
	}

	return strings.TrimSpace(string(result)), nil
}
