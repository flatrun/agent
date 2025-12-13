package docker

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type ComposeExecutor struct {
	basePath string
}

func NewComposeExecutor(basePath string) *ComposeExecutor {
	return &ComposeExecutor{basePath: basePath}
}

func (c *ComposeExecutor) Up(deploymentPath string) (string, error) {
	return c.runCompose(deploymentPath, "up", "-d", "--remove-orphans")
}

func (c *ComposeExecutor) Down(deploymentPath string) (string, error) {
	return c.runCompose(deploymentPath, "down", "--remove-orphans")
}

func (c *ComposeExecutor) Start(deploymentPath string) (string, error) {
	// Try start first for existing containers
	output, err := c.runCompose(deploymentPath, "start")
	if err != nil {
		// Fall back to up if containers don't exist
		return c.runCompose(deploymentPath, "up", "-d", "--remove-orphans")
	}
	return output, nil
}

func (c *ComposeExecutor) Stop(deploymentPath string) (string, error) {
	return c.runCompose(deploymentPath, "stop")
}

func (c *ComposeExecutor) Restart(deploymentPath string) (string, error) {
	// Use down to properly remove containers before recreating
	_, _ = c.runCompose(deploymentPath, "down", "--remove-orphans")
	return c.runCompose(deploymentPath, "up", "-d", "--remove-orphans")
}

func (c *ComposeExecutor) Logs(deploymentPath string, tail int) (string, error) {
	tailStr := fmt.Sprintf("%d", tail)
	return c.runCompose(deploymentPath, "logs", "--tail", tailStr)
}

func (c *ComposeExecutor) PS(deploymentPath string) (string, error) {
	return c.runCompose(deploymentPath, "ps", "--format", "json")
}

type ImageInfo struct {
	Service   string `json:"service"`
	Image     string `json:"image"`
	IsLatest  bool   `json:"is_latest"`
	IsBuild   bool   `json:"is_build"`
}

func (c *ComposeExecutor) Pull(deploymentPath string, onlyLatest bool) (string, error) {
	if onlyLatest {
		services, err := c.getLatestTaggedServices(deploymentPath)
		if err != nil || len(services) == 0 {
			return "", err
		}
		args := []string{"pull", "--ignore-buildable", "--policy", "always"}
		args = append(args, services...)
		return c.runCompose(deploymentPath, args...)
	}
	return c.runCompose(deploymentPath, "pull", "--ignore-buildable", "--policy", "always")
}

func (c *ComposeExecutor) GetImageInfo(deploymentPath string) ([]ImageInfo, error) {
	composePath := c.findComposeFile(deploymentPath)
	if composePath == "" {
		return nil, fmt.Errorf("no compose file found in %s", deploymentPath)
	}

	data, err := os.ReadFile(composePath)
	if err != nil {
		return nil, err
	}

	var compose struct {
		Services map[string]struct {
			Image string      `yaml:"image"`
			Build interface{} `yaml:"build"`
		} `yaml:"services"`
	}

	if err := yaml.Unmarshal(data, &compose); err != nil {
		return nil, err
	}

	var images []ImageInfo
	for name, svc := range compose.Services {
		info := ImageInfo{
			Service: name,
			Image:   svc.Image,
			IsBuild: svc.Build != nil,
		}
		if svc.Image != "" {
			info.IsLatest = isLatestTag(svc.Image)
		}
		images = append(images, info)
	}

	return images, nil
}

func (c *ComposeExecutor) getLatestTaggedServices(deploymentPath string) ([]string, error) {
	images, err := c.GetImageInfo(deploymentPath)
	if err != nil {
		return nil, err
	}

	var services []string
	for _, img := range images {
		if img.IsLatest && !img.IsBuild {
			services = append(services, img.Service)
		}
	}
	return services, nil
}

func isLatestTag(image string) bool {
	if !strings.Contains(image, ":") {
		return true
	}
	parts := strings.Split(image, ":")
	tag := parts[len(parts)-1]
	return tag == "latest"
}

func (c *ComposeExecutor) getProjectName(deploymentPath string) string {
	parts := strings.Split(strings.TrimSuffix(deploymentPath, "/"), "/")
	if len(parts) == 0 {
		return "flatrun"
	}
	dirName := parts[len(parts)-1]

	// First, try to read name from compose file
	if name := c.readComposeProjectName(deploymentPath); name != "" {
		return name
	}

	// Fallback: detect existing project from running containers
	if name := c.detectExistingProject(dirName); name != "" {
		return name
	}

	// Default to directory name for compatibility
	return dirName
}

// readComposeProjectName reads the 'name:' attribute from the compose file
func (c *ComposeExecutor) readComposeProjectName(deploymentPath string) string {
	composePath := c.findComposeFile(deploymentPath)
	if composePath == "" {
		return ""
	}

	data, err := os.ReadFile(composePath)
	if err != nil {
		return ""
	}

	var compose struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal(data, &compose); err == nil && compose.Name != "" {
		return compose.Name
	}
	return ""
}

// findComposeFile finds any compose file in the deployment directory
func (c *ComposeExecutor) findComposeFile(dirPath string) string {
	standardNames := []string{
		"docker-compose.yml",
		"docker-compose.yaml",
		"compose.yml",
		"compose.yaml",
	}

	for _, name := range standardNames {
		path := dirPath + "/" + name
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	patterns := []string{
		"*compose*.yml",
		"*compose*.yaml",
	}

	for _, pattern := range patterns {
		matches, _ := filepath.Glob(dirPath + "/" + pattern)
		if len(matches) > 0 {
			return matches[0]
		}
	}

	return ""
}

// detectExistingProject checks if containers exist with common project name patterns
func (c *ComposeExecutor) detectExistingProject(dirName string) string {
	candidates := []string{
		dirName,
		"flatrun-" + dirName,
	}

	for _, candidate := range candidates {
		cmd := exec.Command("docker", "compose", "-p", candidate, "ps", "-q")
		output, err := cmd.Output()
		if err == nil && len(strings.TrimSpace(string(output))) > 0 {
			return candidate
		}
	}
	return ""
}

func (c *ComposeExecutor) runCompose(deploymentPath string, args ...string) (string, error) {
	composeCmd := c.findComposeCommand()
	if composeCmd == "" {
		return "", fmt.Errorf("docker compose command not found")
	}

	composePath := c.findComposeFile(deploymentPath)
	if composePath == "" {
		return "", fmt.Errorf("no compose file found in %s", deploymentPath)
	}

	projectName := c.getProjectName(deploymentPath)

	var baseArgs []string
	baseArgs = append(baseArgs, "-f", composePath, "-p", projectName)

	envFile := deploymentPath + "/.env.flatrun"
	if _, err := os.Stat(envFile); err == nil {
		baseArgs = append(baseArgs, "--env-file", ".env.flatrun")
	}

	var cmd *exec.Cmd

	if composeCmd == "docker-compose" {
		fullArgs := append(baseArgs, args...)
		cmd = exec.Command(composeCmd, fullArgs...)
	} else {
		fullArgs := append([]string{"compose"}, baseArgs...)
		fullArgs = append(fullArgs, args...)
		cmd = exec.Command("docker", fullArgs...)
	}

	cmd.Dir = deploymentPath

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return stderr.String(), fmt.Errorf("%w: %s", err, stderr.String())
	}

	return stdout.String(), nil
}

func (c *ComposeExecutor) findComposeCommand() string {
	if _, err := exec.LookPath("docker"); err == nil {
		cmd := exec.Command("docker", "compose", "version")
		if err := cmd.Run(); err == nil {
			return "docker"
		}
	}

	if _, err := exec.LookPath("docker-compose"); err == nil {
		return "docker-compose"
	}

	return ""
}

func (c *ComposeExecutor) GetStatus(deploymentPath string) (string, error) {
	output, err := c.PS(deploymentPath)
	if err != nil {
		if strings.Contains(err.Error(), "no such service") ||
			strings.Contains(err.Error(), "no configuration file") {
			return "stopped", nil
		}
		return "error", err
	}

	trimmed := strings.TrimSpace(output)
	if trimmed == "" || trimmed == "[]" {
		return "stopped", nil
	}

	// Check for running state in various formats from docker compose ps
	lower := strings.ToLower(output)
	if strings.Contains(lower, "\"state\":\"running\"") ||
		strings.Contains(lower, "\"state\": \"running\"") ||
		strings.Contains(lower, "running") ||
		strings.Contains(lower, "\"status\":\"up") ||
		strings.Contains(lower, "\"status\": \"up") {
		return "running", nil
	}

	// Check for exited/stopped states
	if strings.Contains(lower, "exited") ||
		strings.Contains(lower, "\"state\":\"exited\"") ||
		strings.Contains(lower, "\"state\": \"exited\"") {
		return "stopped", nil
	}

	return "unknown", nil
}

func (c *ComposeExecutor) ExecCommand(containerID string, command string) (string, error) {
	shells := []string{"/bin/bash", "/bin/sh", "bash", "sh"}

	for _, shell := range shells {
		args := []string{"exec", containerID, shell, "-c", command}
		cmd := exec.Command("docker", args...)

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		output := stdout.String() + stderr.String()

		if err != nil {
			// Only try next shell if the shell itself wasn't found
			lowerOutput := strings.ToLower(output)
			shellNotFound := strings.Contains(lowerOutput, "oci runtime exec failed") ||
				strings.Contains(lowerOutput, fmt.Sprintf("%s: not found", shell)) ||
				strings.Contains(lowerOutput, fmt.Sprintf("%s: no such file", shell)) ||
				strings.Contains(lowerOutput, "executable file not found in $path")
			if shellNotFound {
				continue
			}
			return output, fmt.Errorf("%w: %s", err, output)
		}

		return output, nil
	}

	return "", fmt.Errorf("no compatible shell found in container")
}
