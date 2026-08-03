package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

// Labels compose stamps on every container it creates, used to map a container
// back to its deployment without invoking the compose CLI.
const (
	composeProjectLabel = "com.docker.compose.project"
	composeServiceLabel = "com.docker.compose.service"
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
		filters.Arg("label", fmt.Sprintf("%s=%s", composeProjectLabel, project)),
		filters.Arg("label", fmt.Sprintf("%s=%s", composeServiceLabel, service)),
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

// ContainerPrimaryIP returns the IP address of a project's first running
// container on the given docker network. The agent runs on the host, so a
// service that only exposes ports on an internal compose network (a self-hosted
// object store, say) is reached by dialing the container's address directly.
// When network is empty, the first attached network with an address is used.
func (a *APIClient) ContainerPrimaryIP(ctx context.Context, project, network string) (string, error) {
	f := filters.NewArgs(
		filters.Arg("label", fmt.Sprintf("%s=%s", composeProjectLabel, project)),
		filters.Arg("status", "running"),
	)

	containers, err := a.cli.ContainerList(ctx, container.ListOptions{Filters: f})
	if err != nil {
		return "", fmt.Errorf("failed to list containers: %w", err)
	}
	if len(containers) == 0 {
		return "", fmt.Errorf("no running container found for project %q", project)
	}

	ns := containers[0].NetworkSettings
	if ns == nil {
		return "", fmt.Errorf("container for project %q has no network settings", project)
	}
	if network != "" {
		if n, ok := ns.Networks[network]; ok && n.IPAddress != "" {
			return n.IPAddress, nil
		}
	}
	for _, n := range ns.Networks {
		if n.IPAddress != "" {
			return n.IPAddress, nil
		}
	}
	return "", fmt.Errorf("container for project %q has no network address yet", project)
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
		filters.Arg("label", fmt.Sprintf("%s=%s", composeProjectLabel, project)),
	)

	return a.cli.ContainerList(ctx, container.ListOptions{Filters: f, All: true})
}

// EnsureImage makes an image available locally, pulling it only when it is
// absent so a seeded deploy of an already-pulled image costs nothing.
func (a *APIClient) EnsureImage(ctx context.Context, ref string) error {
	_, err := a.cli.ImageInspect(ctx, ref)
	if err == nil {
		return nil
	}
	if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("failed to inspect image %s: %w", ref, err)
	}

	body, err := a.cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", ref, err)
	}
	defer body.Close()

	// The pull only completes once its progress stream is drained.
	if _, err := io.Copy(io.Discard, body); err != nil {
		return fmt.Errorf("failed to pull image %s: %w", ref, err)
	}
	return nil
}

// CopyPathToHost writes containerPath from an existing container out to
// hostPath. The container does not need to be running, so a caller can stop it
// first and be certain the copy cannot miss a later write.
func (a *APIClient) CopyPathToHost(ctx context.Context, containerID, containerPath, hostPath string) error {
	reader, stat, err := a.cli.CopyFromContainer(ctx, containerID, containerPath)
	if err != nil {
		return fmt.Errorf("failed to read %s from the container: %w", containerPath, err)
	}
	defer reader.Close()

	return extractSeedTar(reader, hostPath, stat.Mode.IsDir())
}

// SeedFromImage copies containerPath out of an image and writes it to hostPath.
// It suits a deployment that has no container yet; for one that is already
// running, copy from the container instead, since an image holds nothing an
// entrypoint generated at runtime.
//
// The container is created but never started: copying reads the image's
// filesystem, so nothing from the image is executed to seed a host path.
func (a *APIClient) SeedFromImage(ctx context.Context, ref, containerPath, hostPath string) error {
	if err := a.EnsureImage(ctx, ref); err != nil {
		return err
	}

	created, err := a.cli.ContainerCreate(ctx, &container.Config{Image: ref}, nil, nil, nil, "")
	if err != nil {
		return fmt.Errorf("failed to create a container to seed from %s: %w", ref, err)
	}
	defer func() {
		_ = a.cli.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true})
	}()

	return a.CopyPathToHost(ctx, created.ID, containerPath, hostPath)
}

// ListLiveComposeContainers returns the containers of every compose project on
// the host in a single call.
//
// It deliberately excludes stopped containers, mirroring `compose ps`, which
// reports only live ones unless asked for all. Including them would both change
// what callers report and cost noticeably more, since the daemon walks every
// container a host has ever left behind.
func (a *APIClient) ListLiveComposeContainers(ctx context.Context) ([]container.Summary, error) {
	f := filters.NewArgs(filters.Arg("label", composeProjectLabel))

	return a.cli.ContainerList(ctx, container.ListOptions{Filters: f})
}
