package runtimes

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/flatrun/agent/internal/guestinit"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

type Firecracker struct {
	Config FirecrackerConfig
	Scope  string
	docker *Docker
}

func NewFirecracker(config FirecrackerConfig, docker *Docker, scope string) *Firecracker {
	if config.DefaultMemory == 0 {
		config.DefaultMemory = 256 << 20
	}
	if config.ChrootBaseDir == "" {
		config.ChrootBaseDir = "/srv/jailer"
	}
	if path, err := filepath.Abs(config.ChrootBaseDir); err == nil {
		config.ChrootBaseDir = path
	}
	if config.StateDir == "" {
		config.StateDir = "/var/lib/flatrun/firecracker"
	}
	if config.Binary == "" {
		config.Binary = "firecracker"
	}
	if config.Jailer == "" {
		config.Jailer = "jailer"
	}
	if config.GuestInit == "" {
		executable, _ := os.Executable()
		config.GuestInit = filepath.Join(filepath.Dir(executable), "flatrun-guest-init")
	}
	if binary, err := exec.LookPath(config.Binary); err == nil {
		if resolved, err := filepath.EvalSymlinks(binary); err == nil {
			config.Binary = resolved
		}
	}
	if path, err := filepath.Abs(config.StateDir); err == nil {
		config.StateDir = path
	}
	return &Firecracker{Config: config, docker: docker, Scope: scope}
}
func (f *Firecracker) Type() Type { return TypeFirecracker }
func (f *Firecracker) Available(ctx context.Context) error {
	if f.Config.Kernel == "" {
		return fmt.Errorf("%w: firecracker: not configured on this node", ErrUnavailable)
	}
	missing := []string{}
	if err := checkDeviceFilesystem(f.Config.ChrootBaseDir); err != nil {
		missing = append(missing, err.Error())
	}
	if err := readableRegularFile(f.Config.Kernel); err != nil {
		missing = append(missing, "kernel is not a readable regular file: "+f.Config.Kernel)
	}
	fd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		missing = append(missing, "pidfd support: "+err.Error())
	} else {
		_ = unix.Close(fd)
	}
	if os.Geteuid() != 0 {
		missing = append(missing, "root privileges")
	}
	if stat, err := os.Stat("/dev/kvm"); err != nil || stat.Mode()&os.ModeDevice == 0 {
		missing = append(missing, "/dev/kvm")
	}
	for _, binary := range []string{f.Config.Binary, f.Config.Jailer, "mkfs.ext4", "ip", "iptables", "ip6tables"} {
		if _, err := exec.LookPath(binary); err != nil {
			missing = append(missing, "binary "+binary)
		}
	}
	if stat, err := os.Stat(f.Config.GuestInit); err != nil || !stat.Mode().IsRegular() || stat.Mode()&0111 == 0 {
		missing = append(missing, "static guest init "+f.Config.GuestInit)
	}
	if len(f.jail(uuid.NewString())+fmt.Sprintf("/v.sock_%d", guestinit.Port)) >= 108 {
		missing = append(missing, "jail socket path exceeds Unix socket limit")
	}
	if f.docker == nil {
		missing = append(missing, "Docker image exporter")
	} else if err := f.docker.Available(ctx); err != nil {
		missing = append(missing, err.Error())
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: firecracker: %s", ErrUnavailable, strings.Join(missing, "; "))
	}
	return nil
}
func (f *Firecracker) scopeKey() string {
	sum := sha256.Sum256([]byte(f.Scope))
	return fmt.Sprintf("%x", sum[:6])
}
func (f *Firecracker) base() string { return filepath.Join(f.Config.ChrootBaseDir, f.scopeKey()) }
func (f *Firecracker) jail(id string) string {
	return filepath.Join(f.base(), filepath.Base(f.Config.Binary), id, "root")
}
func (f *Firecracker) directory(id string) string {
	return filepath.Join(f.Config.StateDir, f.scopeKey(), id)
}

type fcState struct {
	ID     string `json:"id"`
	Scope  string `json:"scope"`
	Jail   string `json:"jail"`
	Jailer string `json:"jailer"`
	Egress bool   `json:"egress"`
	Subnet string `json:"subnet,omitempty"`
}

type firecrackerInstance struct {
	runtime    *Firecracker
	jailPath   string
	jailerName string
	subnet     string
	id         string
	spec       Spec
	command    guestinit.Start
	process    *exec.Cmd
	done       chan struct{}
	mu         sync.Mutex
}

func (i *firecrackerInstance) ID() string { return i.id }
func (i *firecrackerInstance) jail() string {
	if i.jailPath != "" {
		return i.jailPath
	}
	return i.runtime.jail(i.id)
}
func (i *firecrackerInstance) jailer() string {
	if i.jailerName != "" {
		return i.jailerName
	}
	return filepath.Base(i.runtime.Config.Jailer)
}

func (f *Firecracker) Create(ctx context.Context, spec Spec) (instance Instance, result error) {
	if spec.Image == "" {
		return nil, errors.New("image is required")
	}
	id := uuid.NewString()
	dir := f.directory(id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	i := &firecrackerInstance{runtime: f, id: id, spec: spec}
	defer func() {
		if result != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result = errors.Join(result, i.Destroy(cleanup))
		}
	}()
	if err := i.writeState(dir); err != nil {
		return nil, err
	}
	root, command, err := f.prepareRoot(ctx, spec, id)
	if err != nil {
		return nil, err
	}
	i.command = command
	jail := i.jail()
	if err = os.MkdirAll(jail, 0700); err != nil {
		return nil, err
	}
	if err = cloneFile(ctx, root, filepath.Join(jail, "root.ext4"), 0600); err != nil {
		return nil, err
	}
	kernel, err := f.cachedKernel(ctx)
	if err != nil {
		return nil, err
	}
	if err = linkKernel(ctx, kernel, filepath.Join(jail, "kernel")); err != nil {
		return nil, err
	}
	if err = os.Chown(filepath.Join(jail, "root.ext4"), 65534, 65534); err != nil {
		return nil, err
	}
	if spec.Network.Egress {
		if err = i.setupNetwork(ctx); err != nil {
			return nil, err
		}
	}
	if err = i.configure(ctx); err != nil {
		return nil, err
	}
	return i, nil
}

func (i *firecrackerInstance) configure(ctx context.Context) error {
	f := i.runtime
	binary, err := exec.LookPath(f.Config.Binary)
	if err != nil {
		return err
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return err
	}
	cmd := exec.Command(f.Config.Jailer, "--id", i.id, "--exec-file", binary, "--uid", "65534", "--gid", "65534", "--cgroup-version", "2", "--chroot-base-dir", f.base(), "--", "--api-sock", "/api.sock")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	log, err := os.OpenFile(filepath.Join(f.directory(i.id), "vmm.log"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	console, err := os.OpenFile(filepath.Join(f.directory(i.id), "console.log"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer console.Close()
	cmd.Stdout, cmd.Stderr = console, log
	if err = cmd.Start(); err != nil {
		return err
	}
	i.process = cmd
	i.done = make(chan struct{})
	go func() { _ = cmd.Wait(); close(i.done) }()
	api := i.client(false)
	defer api.CloseIdleConnections()
	if err = i.retry(ctx, func() error {
		response, err := i.request(ctx, api, http.MethodGet, "/", nil)
		if err == nil {
			_ = response.Body.Close()
		}
		return err
	}); err != nil {
		return err
	}
	memory := i.spec.Resources.Memory
	if memory == 0 {
		memory = f.Config.DefaultMemory
	}
	cpus := int(math.Ceil(i.spec.Resources.CPUs))
	if cpus == 0 {
		cpus = 1
	}
	boot := "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw init=/flatrun-init"
	if i.spec.Network.Egress {
		boot += " ip=" + i.guestIP() + "::" + i.hostIP() + ":255.255.255.252::eth0:off"
	}
	for _, request := range []struct {
		path string
		body any
	}{
		{"/machine-config", map[string]any{"vcpu_count": cpus, "mem_size_mib": (memory + (1 << 20) - 1) >> 20}},
		{"/boot-source", map[string]any{"kernel_image_path": "/kernel", "boot_args": boot}},
		{"/drives/rootfs", map[string]any{"drive_id": "rootfs", "path_on_host": "/root.ext4", "is_root_device": true, "is_read_only": false}},
		{"/vsock", map[string]any{"guest_cid": 3, "uds_path": "/v.sock"}},
	} {
		if err = i.api(ctx, api, request.path, request.body); err != nil {
			return err
		}
	}
	if i.spec.Network.Egress {
		if err = i.api(ctx, api, "/network-interfaces/eth0", map[string]any{"iface_id": "eth0", "host_dev_name": i.tap(), "guest_mac": "06:00:ac:10:00:02"}); err != nil {
			return err
		}
	}
	return nil
}

func (i *firecrackerInstance) Start(ctx context.Context) error {
	api := i.client(false)
	defer api.CloseIdleConnections()
	var err error
	if err = i.api(ctx, api, "/actions", map[string]string{"action_type": "InstanceStart"}); err != nil {
		return err
	}
	guest := i.client(true)
	defer guest.CloseIdleConnections()
	if err = i.retry(ctx, func() error {
		response, err := i.request(ctx, guest, http.MethodGet, "/logs", nil)
		if err == nil {
			_ = response.Body.Close()
		}
		return err
	}); err != nil {
		return err
	}
	response, err := i.request(ctx, guest, http.MethodPost, "/start", i.command)
	if err != nil {
		return err
	}
	return response.Body.Close()
}
func (i *firecrackerInstance) retry(ctx context.Context, fn func() error) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := fn(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-i.done:
			return i.bootError()
		case <-ticker.C:
		}
	}
}
func (i *firecrackerInstance) api(ctx context.Context, client *http.Client, path string, body any) error {
	response, err := i.request(ctx, client, http.MethodPut, path, body)
	if err != nil {
		return err
	}
	return response.Body.Close()
}
func (i *firecrackerInstance) request(ctx context.Context, client *http.Client, method, path string, body any) (*http.Response, error) {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://guest"+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 300 {
		defer response.Body.Close()
		message, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
		return nil, fmt.Errorf("%s: %s", path, message)
	}
	return response, nil
}
func (i *firecrackerInstance) client(guest bool) *http.Client {
	return &http.Client{Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		path := filepath.Join(i.jail(), "api.sock")
		if guest {
			path = filepath.Join(i.jail(), "v.sock")
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
		if err != nil {
			return nil, err
		}
		if !guest {
			return conn, nil
		}
		deadline := time.Now().Add(time.Second)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		_ = conn.SetDeadline(deadline)
		if _, err = fmt.Fprintf(conn, "CONNECT %d\n", guestinit.Port); err != nil {
			_ = conn.Close()
			return nil, err
		}
		reader := bufio.NewReaderSize(conn, 128)
		line, err := reader.ReadSlice('\n')
		var port uint32
		if err == nil {
			_, err = fmt.Sscanf(string(line), "OK %d\n", &port)
		}
		if err == nil && port == 0 {
			err = errors.New("invalid vsock acknowledgement")
		}
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("vsock handshake: %q: %w", line, err)
		}
		_ = conn.SetDeadline(time.Time{})
		return &bufferedConn{Conn: conn, reader: reader}, nil
	}}}
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
func (i *firecrackerInstance) Wait(ctx context.Context) (Exit, error) {
	client := i.client(true)
	defer client.CloseIdleConnections()
	response, err := i.request(ctx, client, http.MethodGet, "/wait", nil)
	if err != nil {
		return Exit{}, err
	}
	defer response.Body.Close()
	var exit Exit
	err = json.NewDecoder(response.Body).Decode(&exit)
	return exit, err
}
func (i *firecrackerInstance) Logs(ctx context.Context, follow bool) (io.ReadCloser, error) {
	return i.stream(ctx, "/logs?follow="+strconv.FormatBool(follow))
}
func (i *firecrackerInstance) CopyOut(ctx context.Context, path string) (io.ReadCloser, error) {
	return i.stream(ctx, "/copyout?path="+url.QueryEscape(path))
}
func (i *firecrackerInstance) stream(ctx context.Context, path string) (io.ReadCloser, error) {
	client := i.client(true)
	response, err := i.request(ctx, client, http.MethodGet, path, nil)
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	return response.Body, nil
}
func (i *firecrackerInstance) Stop(ctx context.Context, grace time.Duration) error {
	client := i.client(true)
	defer client.CloseIdleConnections()
	response, err := i.request(ctx, client, http.MethodPost, "/stop?grace="+url.QueryEscape(grace.String()), nil)
	if err != nil {
		return err
	}
	return response.Body.Close()
}
func (f *Firecracker) Leftovers(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(f.Config.StateDir, f.scopeKey()))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := uuid.Parse(entry.Name()); err != nil || !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(f.directory(entry.Name()), "state.json"))
		if errors.Is(err, os.ErrNotExist) {
			ids = append(ids, entry.Name())
			continue
		}
		if err != nil {
			return nil, err
		}
		var state fcState
		if err = json.Unmarshal(data, &state); err != nil {
			ids = append(ids, entry.Name())
			continue
		}
		if state.ID == entry.Name() && state.Scope == f.Scope {
			ids = append(ids, state.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
func (f *Firecracker) DestroyByID(ctx context.Context, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return err
	}
	ids, err := f.Leftovers(ctx)
	if err != nil {
		return err
	}
	for _, candidate := range ids {
		if id == candidate {
			instance := &firecrackerInstance{runtime: f, id: id}
			data, err := os.ReadFile(filepath.Join(f.directory(id), "state.json"))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			var state fcState
			if json.Unmarshal(data, &state) == nil {
				if state.Jail != "" {
					if filepath.Base(state.Jail) != "root" || filepath.Base(filepath.Dir(state.Jail)) != id || filepath.Dir(filepath.Dir(filepath.Dir(state.Jail))) != f.base() {
						return errors.New("invalid jail path in instance state")
					}
					instance.jailPath = state.Jail
				}
				instance.jailerName = state.Jailer
				instance.spec.Network.Egress = state.Egress
				if state.Subnet != "" {
					prefix, err := netip.ParsePrefix(state.Subnet)
					if err != nil || prefix.Bits() != 30 || !netip.MustParsePrefix("100.64.0.0/10").Contains(prefix.Addr()) {
						return errors.New("invalid subnet in instance state")
					}
				}
				instance.subnet = state.Subnet
			}
			return instance.Destroy(ctx)
		}
	}
	return nil
}
func (i *firecrackerInstance) Destroy(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if i.process != nil {
		if err := i.process.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		select {
		case <-i.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		fd, err := unix.PidfdOpen(pid, 0)
		if errors.Is(err, unix.ESRCH) {
			continue
		}
		if err != nil {
			return err
		}
		root, readErr := os.Readlink(filepath.Join("/proc", entry.Name(), "root"))
		data, _ := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		args := strings.Split(string(data), "\x00")
		matches := readErr == nil && root == i.jail()
		if !matches && len(args) > 0 && filepath.Base(args[0]) == i.jailer() {
			idMatches, scopeMatches := false, false
			for n := 0; n+1 < len(args); n++ {
				if args[n] == "--id" && args[n+1] == i.id {
					idMatches = true
				}
				if args[n] == "--chroot-base-dir" && args[n+1] == i.runtime.base() {
					scopeMatches = true
				}
			}
			matches = idMatches && scopeMatches
		}
		if matches {
			err = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
			if err == nil {
				for {
					ready, pollErr := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 20)
					if pollErr != nil && !errors.Is(pollErr, unix.EINTR) {
						err = pollErr
						break
					}
					if ready > 0 {
						break
					}
					if err = ctx.Err(); err != nil {
						break
					}
				}
			}
		}
		_ = unix.Close(fd)
		if err != nil && !errors.Is(err, unix.ESRCH) {
			return err
		}
	}
	if i.done != nil {
		select {
		case <-i.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var errs []error
	if f := i.runtime; f.docker != nil {
		errs = append(errs, f.exporter().DestroyByID(ctx, i.id))
	}
	if i.spec.Network.Egress {
		errs = append(errs, i.cleanupNetwork(ctx))
	}
	errs = append(errs, os.RemoveAll(filepath.Dir(i.jail())))
	errs = append(errs, removeEmptyParents(filepath.Dir(filepath.Dir(i.jail())), i.runtime.base()))
	errs = append(errs, i.cleanupCgroup())
	if err = errors.Join(errs...); err != nil {
		return err
	}
	if err = os.RemoveAll(i.runtime.directory(i.id)); err != nil {
		return err
	}
	scope := filepath.Dir(i.runtime.directory(i.id))
	return removeEmptyParents(scope, scope)
}

func copyFile(ctx context.Context, source, target string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer output.Close()
	_, err = io.Copy(output, contextReader{ctx: ctx, reader: input})
	if err != nil {
		return err
	}
	return output.Sync()
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (i *firecrackerInstance) writeState(directory string) error {
	data, err := json.Marshal(fcState{ID: i.id, Scope: i.runtime.Scope, Jail: i.jail(), Jailer: i.jailer(), Egress: i.spec.Network.Egress, Subnet: i.subnet})
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(directory, "state.tmp"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(filepath.Join(directory, "state.tmp"), filepath.Join(directory, "state.json")); err != nil {
		return err
	}
	for _, path := range []string{directory, filepath.Dir(directory)} {
		dir, err := os.Open(path)
		if err != nil {
			return err
		}
		err = dir.Sync()
		err = errors.Join(err, dir.Close())
		if err != nil {
			return err
		}
	}
	return nil
}

func readableRegularFile(path string) error {
	stat, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !stat.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func checkDeviceFilesystem(path string) error {
	original := path
	for {
		var stat unix.Statfs_t
		err := unix.Statfs(path, &stat)
		if err == nil {
			if stat.Flags&unix.ST_NODEV != 0 {
				return fmt.Errorf("chroot base %s is on a nodev filesystem", original)
			}
			return nil
		}
		if !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("chroot base %s: %w", original, err)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return err
		}
		path = parent
	}
}
func removeEmptyParents(path, stop string) error {
	for {
		err := os.Remove(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			if errors.Is(err, unix.ENOTEMPTY) || errors.Is(err, unix.EEXIST) {
				return nil
			}
			return err
		}
		if path == stop {
			return nil
		}
		path = filepath.Dir(path)
	}
}
func (i *firecrackerInstance) cleanupCgroup() error {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[2] != "cgroup2" {
			continue
		}
		parent := filepath.Join(fields[1], filepath.Base(filepath.Dir(filepath.Dir(i.jail()))))
		if err = os.Remove(filepath.Join(parent, i.id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
func cloneFile(ctx context.Context, source, target string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer output.Close()
	if err = ctx.Err(); err != nil {
		return err
	}
	err = unix.IoctlFileClone(int(output.Fd()), int(input.Fd()))
	if err != nil {
		if !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.EXDEV) && !errors.Is(err, unix.ENOTTY) && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOSYS) {
			return err
		}
		if err = copySparseFile(ctx, input, output); err != nil {
			return err
		}
	}
	return output.Sync()
}
func copySparseFile(ctx context.Context, input, output *os.File) error {
	stat, err := input.Stat()
	if err != nil {
		return err
	}
	for offset := int64(0); offset < stat.Size(); {
		if err = ctx.Err(); err != nil {
			return err
		}
		data, seekErr := unix.Seek(int(input.Fd()), offset, unix.SEEK_DATA)
		if errors.Is(seekErr, unix.ENXIO) {
			break
		}
		if seekErr != nil {
			return seekErr
		}
		hole, seekErr := unix.Seek(int(input.Fd()), data, unix.SEEK_HOLE)
		if seekErr != nil {
			return seekErr
		}
		if hole <= data {
			return errors.New("invalid sparse file extent")
		}
		if _, err = input.Seek(data, io.SeekStart); err != nil {
			return err
		}
		if _, err = output.Seek(data, io.SeekStart); err != nil {
			return err
		}
		if _, err = io.CopyN(output, contextReader{ctx: ctx, reader: input}, min(hole, stat.Size())-data); err != nil {
			return err
		}
		offset = hole
	}
	return output.Truncate(stat.Size())
}

func linkKernel(ctx context.Context, source, target string) error {
	stat, err := os.Stat(source)
	if err != nil {
		return err
	}
	// A linked kernel must already be read-only: chmod or chown would alter the source inode.
	if stat.Mode().Perm()&0222 == 0 && stat.Mode().Perm()&0004 != 0 {
		if err = os.Link(source, target); err == nil {
			return ctx.Err()
		}
		if !errors.Is(err, unix.EXDEV) && !errors.Is(err, unix.EPERM) && !errors.Is(err, unix.EACCES) && !errors.Is(err, unix.EOPNOTSUPP) {
			return err
		}
	}
	return copyFile(ctx, source, target, 0444)
}

func (i *firecrackerInstance) bootError() error {
	message := "firecracker exited during boot"
	for _, name := range []string{"console.log", "vmm.log"} {
		tail, err := bootLogTail(filepath.Join(i.runtime.directory(i.id), name))
		if err != nil {
			message += fmt.Sprintf("\n%s: %v", name, err)
			continue
		}
		if tail != "" {
			message += "\n" + name + ":\n" + tail
		}
	}
	return errors.New(message)
}

func bootLogTail(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return "", err
	}
	offset := max(int64(0), stat.Size()-4096)
	if _, err = file.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(file, 4096))
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\r\n"), "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	return strings.Join(lines, "\n"), nil
}
