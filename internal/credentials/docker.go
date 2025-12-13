package credentials

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/flatrun/agent/pkg/models"
)

func testDockerLogin(rt *models.RegistryType, cred *models.RegistryCredential) error {
	registry := rt.URLPatterns[0]
	if registry == "docker.io" {
		registry = ""
	}

	cmd := exec.Command("docker", "login",
		"--username", cred.Username,
		"--password-stdin",
	)

	if registry != "" {
		cmd.Args = append(cmd.Args, registry)
	}

	cmd.Stdin = strings.NewReader(cred.Password)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("login failed: %s", strings.TrimSpace(string(output)))
	}

	return nil
}

func DockerLogin(registry, username, password string) error {
	args := []string{"login", "--username", username, "--password-stdin"}

	if registry != "" && registry != "docker.io" {
		args = append(args, registry)
	}

	cmd := exec.Command("docker", args...)
	cmd.Stdin = strings.NewReader(password)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker login failed: %s", strings.TrimSpace(string(output)))
	}

	return nil
}

func DockerLogout(registry string) error {
	args := []string{"logout"}

	if registry != "" && registry != "docker.io" {
		args = append(args, registry)
	}

	cmd := exec.Command("docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker logout failed: %s", strings.TrimSpace(string(output)))
	}

	return nil
}

func PullImageWithAuth(imageName string, cred *models.RegistryCredential) error {
	registry := extractRegistry(imageName)

	if cred != nil {
		if err := DockerLogin(registry, cred.Username, cred.Password); err != nil {
			return fmt.Errorf("authentication failed: %w", err)
		}
	}

	cmd := exec.Command("docker", "pull", imageName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to pull image: %s", strings.TrimSpace(string(output)))
	}

	return nil
}
