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
	return c.runCompose(deploymentPath, "up", "-d")
}

func (c *ComposeExecutor) Down(deploymentPath string) (string, error) {
	return c.runCompose(deploymentPath, "down")
}

func (c *ComposeExecutor) Start(deploymentPath string) (string, error) {
	return c.runCompose(deploymentPath, "start")
}

func (c *ComposeExecutor) Stop(deploymentPath string) (string, error) {
	return c.runCompose(deploymentPath, "stop")
}

func (c *ComposeExecutor) Restart(deploymentPath string) (string, error) {
	return c.runCompose(deploymentPath, "restart")
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

func (c *ComposeExecutor) runCompose(deploymentPath string, args ...string) (string, error) {
	composeCmd := c.findComposeCommand()
	if composeCmd == "" {
		return "", fmt.Errorf("docker compose command not found")
	}

	var cmd *exec.Cmd

	if composeCmd == "docker-compose" {
		cmd = exec.Command(composeCmd, args...)
	} else {
		fullArgs := append([]string{"compose"}, args...)
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

	if strings.TrimSpace(output) == "" || strings.TrimSpace(output) == "[]" {
		return "stopped", nil
	}

	if strings.Contains(output, "running") {
		return "running", nil
	}

	return "unknown", nil
}
