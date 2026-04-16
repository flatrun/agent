package credentials

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

func PullImageWithAuth(imageName string, cred *models.RegistryCredential) error {
	cmd := exec.Command("docker", "pull", imageName)

	if cred != nil {
		dir, err := writeEphemeralAuth(extractRegistry(imageName), cred)
		if err != nil {
			return fmt.Errorf("authentication setup failed: %w", err)
		}
		defer os.RemoveAll(dir)
		cmd.Env = append(os.Environ(), "DOCKER_CONFIG="+dir)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to pull image: %s", strings.TrimSpace(string(output)))
	}

	return nil
}

func writeEphemeralAuth(registry string, cred *models.RegistryCredential) (string, error) {
	host := cred.RegistryURL
	if host == "" {
		host = registry
	}
	if host == "" {
		host = "docker.io"
	}
	key := host
	if key == "docker.io" || key == "index.docker.io" || key == "registry-1.docker.io" {
		key = dockerHubAuthKey
	}
	auths := map[string]dockerAuthEntry{
		key: {Auth: base64.StdEncoding.EncodeToString([]byte(cred.Username + ":" + cred.Password))},
	}
	dir, err := os.MkdirTemp("", "flatrun-docker-auth-*")
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(dockerAuthFile{Auths: auths})
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0600); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}
