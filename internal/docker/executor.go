package docker

import (
	"fmt"
	"os/exec"

	"github.com/flatrun/agent/pkg/config"
)

type ServiceExecutor struct {
	config *config.ServiceExecConfig
}

func NewServiceExecutor(cfg *config.ServiceExecConfig) *ServiceExecutor {
	return &ServiceExecutor{config: cfg}
}

func (e *ServiceExecutor) Execute(args []string) ([]byte, error) {
	if e.config.ShouldUseDockerRun() {
		return e.executeWithDockerRun(args)
	}
	return e.executeWithDockerExec(args)
}

func (e *ServiceExecutor) executeWithDockerRun(args []string) ([]byte, error) {
	image := e.config.Image
	if image == "" {
		return nil, fmt.Errorf("image is required for docker run")
	}

	dockerArgs := []string{"run", "--rm"}

	for _, vol := range e.config.Volumes {
		dockerArgs = append(dockerArgs, "-v", vol)
	}

	for _, net := range e.config.Networks {
		dockerArgs = append(dockerArgs, "--network", net)
	}

	dockerArgs = append(dockerArgs, image)
	dockerArgs = append(dockerArgs, args...)

	cmd := exec.Command("docker", dockerArgs...)
	return cmd.CombinedOutput()
}

func (e *ServiceExecutor) executeWithDockerExec(args []string) ([]byte, error) {
	container := e.config.Container
	if container == "" {
		return nil, fmt.Errorf("container name is required for docker exec")
	}

	dockerArgs := []string{"exec", container}
	dockerArgs = append(dockerArgs, args...)

	cmd := exec.Command("docker", dockerArgs...)
	return cmd.CombinedOutput()
}

func ExecuteService(cfg *config.ServiceExecConfig, args []string) ([]byte, error) {
	executor := NewServiceExecutor(cfg)
	return executor.Execute(args)
}
