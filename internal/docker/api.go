package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

type APIClient struct {
	cli *client.Client
}

func NewAPIClient() (*APIClient, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}
	return &APIClient{cli: cli}, nil
}

func (a *APIClient) Close() error {
	return a.cli.Close()
}

func (a *APIClient) FindContainer(ctx context.Context, project, service string) (string, error) {
	f := filters.NewArgs(
		filters.Arg("label", fmt.Sprintf("com.docker.compose.project=%s", project)),
		filters.Arg("label", fmt.Sprintf("com.docker.compose.service=%s", service)),
		filters.Arg("status", "running"),
	)

	containers, err := a.cli.ContainerList(ctx, container.ListOptions{Filters: f})
	if err != nil {
		return "", fmt.Errorf("failed to list containers: %w", err)
	}

	if len(containers) == 0 {
		return "", fmt.Errorf("no running container found for service '%s' in project '%s'", service, project)
	}

	return containers[0].ID, nil
}

func (a *APIClient) ExecInContainer(ctx context.Context, containerID string, command string) (string, error) {
	execConfig := container.ExecOptions{
		Cmd:          []string{"sh", "-c", command},
		AttachStdout: true,
		AttachStderr: true,
	}

	execID, err := a.cli.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return "", fmt.Errorf("failed to create exec: %w", err)
	}

	resp, err := a.cli.ContainerExecAttach(ctx, execID.ID, container.ExecStartOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to attach exec: %w", err)
	}
	defer resp.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, resp.Reader); err != nil {
		return "", fmt.Errorf("failed to read exec output: %w", err)
	}

	deadline := time.After(5 * time.Second)
	for {
		inspect, err := a.cli.ContainerExecInspect(ctx, execID.ID)
		if err != nil {
			return buf.String(), fmt.Errorf("failed to inspect exec: %w", err)
		}
		if !inspect.Running {
			if inspect.ExitCode != 0 {
				return buf.String(), fmt.Errorf("command exited with code %d", inspect.ExitCode)
			}
			break
		}
		select {
		case <-deadline:
			return buf.String(), fmt.Errorf("timed out waiting for exec to finish")
		case <-ctx.Done():
			return buf.String(), ctx.Err()
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}

	return stripDockerStreamHeaders(buf.Bytes()), nil
}

const dockerStreamHeaderSize = 8

func stripDockerStreamHeaders(data []byte) string {
	var result strings.Builder
	for len(data) >= dockerStreamHeaderSize {
		size := int(binary.BigEndian.Uint32(data[4:8]))
		if dockerStreamHeaderSize+size > len(data) {
			break
		}
		result.Write(data[dockerStreamHeaderSize : dockerStreamHeaderSize+size])
		data = data[dockerStreamHeaderSize+size:]
	}
	if result.Len() == 0 {
		return string(data)
	}
	return result.String()
}

func (a *APIClient) ExecInService(ctx context.Context, project, service, command string) (string, error) {
	containerID, err := a.FindContainer(ctx, project, service)
	if err != nil {
		return "", err
	}
	return a.ExecInContainer(ctx, containerID, command)
}

func (a *APIClient) ListServiceContainers(ctx context.Context, project string) ([]container.Summary, error) {
	f := filters.NewArgs(
		filters.Arg("label", fmt.Sprintf("com.docker.compose.project=%s", project)),
	)

	return a.cli.ContainerList(ctx, container.ListOptions{Filters: f, All: true})
}
