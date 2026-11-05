package runtimes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/google/uuid"
)

// Docker runs each instance as one container. A container shares the host
// kernel, so it is not a boundary for untrusted code; Firecracker is.
type Docker struct {
	cli *client.Client
	mu  sync.Mutex

	// EgressSubnet pins the egress network to a CIDR. Leave empty to let the
	// daemon choose; set it on hosts whose default address pools are used up.
	EgressSubnet string
	Scope        string
}

func NewDocker() (*Docker, error) {
	return NewDockerWithHost("")
}

func NewDockerWithHost(host string) (*Docker, error) {
	opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Docker{cli: cli}, nil
}

func (d *Docker) Type() Type { return TypeDocker }

func (d *Docker) Available(ctx context.Context) error {
	if _, err := d.cli.Ping(ctx); err != nil {
		return fmt.Errorf("%w: docker daemon unreachable: %v", ErrUnavailable, err)
	}
	return nil
}

func (d *Docker) Create(ctx context.Context, spec Spec) (Instance, error) {
	if spec.Image == "" {
		return nil, errors.New("image is required")
	}
	if err := d.ensureImage(ctx, spec.Image); err != nil {
		return nil, err
	}

	id := uuid.NewString()
	labels := make(map[string]string, len(spec.Labels)+2)
	for k, v := range spec.Labels {
		labels[k] = v
	}
	labels[Label] = id
	labels[ScopeLabel] = d.Scope
	inst := &dockerInstance{cli: d.cli, id: id}

	networkMode := container.NetworkMode("none")
	if spec.Network.Egress {
		if err := d.ensureEgressNetwork(ctx); err != nil {
			return nil, err
		}
		networkMode = container.NetworkMode(EgressNetwork)
	}

	pids := int64(1024)
	created, err := d.cli.ContainerCreate(ctx,
		&container.Config{
			Image:      spec.Image,
			Cmd:        spec.Command,
			Env:        envList(spec.Env),
			WorkingDir: spec.WorkDir,
			Labels:     labels,
		},
		&container.HostConfig{
			NetworkMode: networkMode,
			SecurityOpt: []string{"no-new-privileges"},
			Resources: container.Resources{
				NanoCPUs:  int64(spec.Resources.CPUs * 1e9),
				Memory:    int64(spec.Resources.Memory),
				PidsLimit: &pids,
			},
		},
		nil, nil, "flatrun-run-"+id)
	if err != nil {
		return nil, fmt.Errorf("create container: %w", err)
	}
	inst.container = created.ID
	return inst, nil
}

// EgressNetwork is shared by every instance that may reach the outside.
// Inter-container traffic is disabled on it, so instances cannot reach each
// other, and one shared bridge does not use up a subnet per instance.
const EgressNetwork = "flatrun-runs-egress"

func (d *Docker) ensureEgressNetwork(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.cli.NetworkInspect(ctx, EgressNetwork, network.InspectOptions{}); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("inspect egress network: %w", err)
	}
	opts := network.CreateOptions{
		Driver:  "bridge",
		Options: map[string]string{"com.docker.network.bridge.enable_icc": "false"},
	}
	if d.EgressSubnet != "" {
		opts.IPAM = &network.IPAM{Config: []network.IPAMConfig{{Subnet: d.EgressSubnet}}}
	}
	_, err := d.cli.NetworkCreate(ctx, EgressNetwork, opts)
	if err != nil && !cerrdefs.IsConflict(err) {
		return fmt.Errorf("create egress network: %w", err)
	}
	return nil
}

func (d *Docker) ensureImage(ctx context.Context, ref string) error {
	if _, err := d.cli.ImageInspect(ctx, ref); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("inspect image: %w", err)
	}
	pull, err := d.cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull image: %w", err)
	}
	defer pull.Close()
	if err := decodeImagePull(pull); err != nil {
		return fmt.Errorf("pull image: %w", err)
	}
	return nil
}

func decodeImagePull(stream io.Reader) error {
	return jsonmessage.DisplayJSONMessagesStream(stream, io.Discard, 0, false, nil)
}

func (d *Docker) Leftovers(ctx context.Context) ([]string, error) {
	f := filters.NewArgs(filters.Arg("label", Label), filters.Arg("label", ScopeLabel+"="+d.Scope))
	containers, err := d.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(containers))
	for _, c := range containers {
		ids = append(ids, c.Labels[Label])
	}
	sort.Strings(ids)
	return ids, nil
}

func (d *Docker) DestroyByID(ctx context.Context, id string) error {
	f := filters.NewArgs(filters.Arg("label", Label+"="+id), filters.Arg("label", ScopeLabel+"="+d.Scope))
	containers, err := d.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range containers {
		inst := &dockerInstance{cli: d.cli, id: id, container: c.ID}
		errs = append(errs, inst.Destroy(ctx))
	}
	return errors.Join(errs...)
}

type dockerInstance struct {
	cli       *client.Client
	id        string
	container string
	mu        sync.Mutex
}

func (i *dockerInstance) ID() string { return i.id }

func (i *dockerInstance) Start(ctx context.Context) error {
	return i.cli.ContainerStart(ctx, i.container, container.StartOptions{})
}

func (i *dockerInstance) Wait(ctx context.Context) (Exit, error) {
	result, errs := i.cli.ContainerWait(ctx, i.container, container.WaitConditionNotRunning)
	select {
	case r := <-result:
		if r.Error != nil {
			return Exit{Code: int(r.StatusCode)}, errors.New(r.Error.Message)
		}
		return Exit{Code: int(r.StatusCode)}, nil
	case err := <-errs:
		return Exit{}, err
	}
}

func (i *dockerInstance) Logs(ctx context.Context, follow bool) (io.ReadCloser, error) {
	raw, err := i.cli.ContainerLogs(ctx, i.container, container.LogsOptions{ShowStdout: true, ShowStderr: true, Follow: follow})
	if err != nil {
		return nil, err
	}
	reader, writer := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(writer, writer, raw)
		raw.Close()
		writer.CloseWithError(err)
	}()
	return reader, nil
}

func (i *dockerInstance) CopyOut(ctx context.Context, path string) (io.ReadCloser, error) {
	stream, _, err := i.cli.CopyFromContainer(ctx, i.container, path)
	return stream, err
}

func (i *dockerInstance) Stop(ctx context.Context, grace time.Duration) error {
	seconds := int(grace.Seconds())
	err := i.cli.ContainerStop(ctx, i.container, container.StopOptions{Timeout: &seconds})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	return err
}

func (i *dockerInstance) Destroy(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.container == "" {
		return nil
	}
	err := i.cli.ContainerRemove(ctx, i.container, container.RemoveOptions{Force: true, RemoveVolumes: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove container: %w", err)
	}
	i.container = ""
	return nil
}

func envList(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}
