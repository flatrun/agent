package docker

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
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
	// Stop then start to handle both existing and new containers
	c.runCompose(deploymentPath, "stop")
	return c.runCompose(deploymentPath, "up", "-d", "--remove-orphans")
}

func (c *ComposeExecutor) Logs(deploymentPath string, tail int) (string, error) {
	tailStr := fmt.Sprintf("%d", tail)
	return c.runCompose(deploymentPath, "logs", "--tail", tailStr)
}

func (c *ComposeExecutor) PS(deploymentPath string) (string, error) {
	return c.runCompose(deploymentPath, "ps", "--format", "json")
}

func (c *ComposeExecutor) Pull(deploymentPath string) (string, error) {
	return c.runCompose(deploymentPath, "pull")
}

func (c *ComposeExecutor) getProjectName(deploymentPath string) string {
	parts := strings.Split(strings.TrimSuffix(deploymentPath, "/"), "/")
	if len(parts) > 0 {
		return "flatrun-" + parts[len(parts)-1]
	}
	return "flatrun"
}

func (c *ComposeExecutor) runCompose(deploymentPath string, args ...string) (string, error) {
	composeCmd := c.findComposeCommand()
	if composeCmd == "" {
		return "", fmt.Errorf("docker compose command not found")
	}

	projectName := c.getProjectName(deploymentPath)

	var cmd *exec.Cmd

	if composeCmd == "docker-compose" {
		fullArgs := append([]string{"-p", projectName}, args...)
		cmd = exec.Command(composeCmd, fullArgs...)
	} else {
		fullArgs := append([]string{"compose", "-p", projectName}, args...)
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
