package runtimes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const TypeTart Type = "tart"

type TartImage struct {
	Base    string `yaml:"base" json:"base"`
	Rosetta bool   `yaml:"rosetta" json:"rosetta"`
}

type TartConfig struct {
	Images map[string]TartImage `yaml:"images" json:"images,omitempty"`
}

type Tart struct {
	TartConfig
	Scope string
}

func (t *Tart) Type() Type { return TypeTart }

func tartCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "tart", args...)
	cmd.Env = append(os.Environ(), "TART_NO_AUTO_PRUNE=1")
	return cmd
}

func (t *Tart) Available(ctx context.Context) error {
	if (runtime.GOOS != "darwin" || runtime.GOARCH != "arm64") || len(t.Images) == 0 {
		return fmt.Errorf("%w: Tart requires an Apple silicon host and configured base images", ErrUnavailable)
	}
	if err := tartCommand(ctx, "--version").Run(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

func (t *Tart) prefix() string { return "flatrun-" + t.Scope + "-" }

func (t *Tart) owned(id string) bool {
	suffix, ok := strings.CutPrefix(id, t.prefix())
	u, err := uuid.Parse(suffix)
	return ok && err == nil && u.String() == suffix
}

func (t *Tart) Leftovers(ctx context.Context) ([]string, error) {
	out, err := tartCommand(ctx, "list", "--source", "local", "--quiet").Output()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, name := range strings.Split(string(out), "\n") {
		if t.owned(name) {
			names = append(names, name)
		}
	}
	return names, nil
}

func (t *Tart) DestroyByID(ctx context.Context, id string) error {
	if !t.owned(id) {
		return errors.New("invalid Tart instance identifier")
	}
	names, err := t.Leftovers(ctx)
	if err != nil {
		return err
	}
	for _, name := range names {
		if name == id {
			_ = tartCommand(ctx, "stop", id).Run()
			return tartCommand(ctx, "delete", id).Run()
		}
	}
	return nil
}

var localTartName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var shellEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (t *Tart) Create(ctx context.Context, s Spec) (Instance, error) {
	img, ok := t.Images[s.Image]
	if !ok || !localTartName.MatchString(img.Base) || strings.HasPrefix(img.Base, "flatrun-"+t.Scope+"-") {
		return nil, errors.New("unknown or invalid Tart base image")
	}
	if s.CacheKey != "" || s.AllowPrivilegeEscalation {
		return nil, errors.New("Tart does not support persistent caches or host privilege escalation")
	}
	if len(s.Command) == 0 {
		return nil, errors.New("Tart runs require a command")
	}
	for key := range s.Env {
		if !shellEnvName.MatchString(key) {
			return nil, errors.New("invalid environment variable name")
		}
	}
	if s.Resources.CPUs != math.Trunc(s.Resources.CPUs) || s.Resources.Memory%(1024*1024) != 0 {
		return nil, errors.New("Tart requires whole CPUs and whole MiB of memory")
	}
	id := t.prefix() + uuid.NewString()
	script := "set -e\n"
	for k, v := range s.Env {
		script += "export " + k + "=" + tartQuote(v) + "\n"
	}
	if s.WorkDir != "" {
		script += "cd " + tartQuote(s.WorkDir) + "\n"
	}
	script += "exec"
	for _, arg := range s.Command {
		script += " " + tartQuote(arg)
	}
	i := &tartInstance{runtime: t, id: id, image: img, egress: s.Network.Egress, script: script + "\n", vmDone: make(chan struct{}), done: make(chan struct{})}
	if err := tartCommand(ctx, "clone", img.Base, id).Run(); err != nil {
		return i, fmt.Errorf("clone Tart base: %w", err)
	}
	args := []string{"set", id}
	if s.Resources.CPUs > 0 {
		args = append(args, "--cpu", strconv.FormatFloat(s.Resources.CPUs, 'f', 0, 64))
	}
	if s.Resources.Memory > 0 {
		args = append(args, "--memory", strconv.FormatUint(s.Resources.Memory/(1024*1024), 10))
	}
	if len(args) > 2 {
		if err := tartCommand(ctx, args...).Run(); err != nil {
			return i, err
		}
	}
	return i, nil
}

func tartQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

type tartInstance struct {
	runtime      *Tart
	id           string
	image        TartImage
	egress       bool
	script       string
	vmDone, done chan struct{}
	logs         *io.PipeReader
	exit         Exit
	err          error
}

func (i *tartInstance) ID() string { return i.id }
func (i *tartInstance) Start(ctx context.Context) error {
	args := []string{"run", "--no-graphics", "--no-clipboard", "--no-usb-accessories", "--no-audio"}
	if i.egress {
		args = append(args, "--net-softnet")
	} else {
		args = append(args, "--net-fd=3")
	}
	if i.image.Rosetta {
		args = append(args, "--rosetta=rosetta")
	}
	vm := tartCommand(context.WithoutCancel(ctx), append(args, i.id)...)
	vm.Stderr = os.Stderr
	vm.Stdout = os.Stderr
	var sink *os.File
	if !i.egress {
		endpoint, peer, err := offlineTartNetwork()
		if err != nil {
			return err
		}
		defer endpoint.Close()
		vm.ExtraFiles = []*os.File{endpoint}
		sink = peer
	}
	if err := vm.Start(); err != nil {
		if sink != nil {
			sink.Close()
		}
		return err
	}
	if sink != nil {
		go func() { _, _ = io.Copy(io.Discard, sink) }()
	}
	go func() {
		_ = vm.Wait()
		if sink != nil {
			sink.Close()
		}
		close(i.vmDone)
	}()
	boot, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	for {
		if err := tartCommand(boot, "exec", i.id, "true").Run(); err == nil {
			break
		}
		select {
		case <-boot.Done():
			return boot.Err()
		case <-i.vmDone:
			return errors.New("Tart VM exited before guest startup")
		case <-time.After(time.Second):
		}
	}
	cmd := tartCommand(ctx, "exec", "-i", i.id, "sh", "-c", startTartGuestJob, "flatrun", i.id)
	cmd.Stdin = strings.NewReader(i.script)
	i.script = ""
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("start guest job: %w: %s", err, output)
	}
	var writer *io.PipeWriter
	i.logs, writer = io.Pipe()
	go func() {
		i.exit, i.err = i.monitor(ctx, writer)
		writer.Close()
		close(i.done)
	}()
	return nil
}

// The guest service owns the job so a dropped guest-agent connection cannot terminate a build.
const startTartGuestJob = `set -eu
umask 077
dir="$HOME/.$1"
mkdir "$dir"
cd "$dir"
cat > task
: > output
cat > launch <<'JOB'
#!/bin/sh
set -eu
dir="$(dirname "$0")"
cd "$HOME"
code=0
sh "$dir/task" >"$dir/output" 2>&1 || code=$?
cd "$dir"
printf '%s\n' "$code" > result.tmp
mv result.tmp result
JOB
chmod 700 launch
if [ "$(uname -s)" = Darwin ]; then
  launchctl submit -l "$1" -- /usr/bin/env "PATH=$PATH" "$dir/launch"
else
  sudo -n systemd-run --quiet --unit="$1" --uid="$(id -u)" --setenv="HOME=$HOME" --setenv="PATH=$PATH" "$dir/launch"
fi`

const readTartGuestJob = `set -eu
cd "$HOME/.$1"
if [ -f result ]; then head -c 12 result; else printf 'running\n'; fi
tail -c "+$2" output | head -c 131072`

func (i *tartInstance) monitor(ctx context.Context, writer io.Writer) (Exit, error) {
	offset := 1
	for {
		sample, cancel := context.WithTimeout(ctx, 15*time.Second)
		out, err := tartCommand(sample, "exec", i.id, "sh", "-c", readTartGuestJob, "flatrun", i.id, strconv.Itoa(offset)).Output()
		cancel()
		if err == nil {
			status, data, ok := bytes.Cut(out, []byte("\n"))
			if !ok {
				return Exit{}, errors.New("invalid guest job status")
			}
			if _, err = writer.Write(data); err != nil {
				return Exit{}, err
			}
			offset += len(data)
			if len(data) == 131072 {
				continue
			}
			if string(status) != "running" {
				code, err := strconv.Atoi(string(status))
				if err != nil || code < 0 || code > 255 {
					return Exit{}, errors.New("invalid guest exit code")
				}
				return Exit{Code: code}, nil
			}
		}
		select {
		case <-ctx.Done():
			return Exit{}, ctx.Err()
		case <-i.vmDone:
			return Exit{}, errors.New("Tart VM exited during the run")
		case <-time.After(2 * time.Second):
		}
	}
}

func (i *tartInstance) Wait(ctx context.Context) (Exit, error) {
	select {
	case <-ctx.Done():
		return Exit{}, ctx.Err()
	case <-i.done:
		return i.exit, i.err
	}
}
func (i *tartInstance) Logs(ctx context.Context, _ bool) (io.ReadCloser, error) {
	context.AfterFunc(ctx, func() { i.logs.CloseWithError(ctx.Err()) })
	return i.logs, nil
}
func (i *tartInstance) CopyOut(ctx context.Context, file string) (io.ReadCloser, error) {
	cmd := tartCommand(ctx, "exec", i.id, "tar", "-C", path.Dir(file), "-cf", "-", "./"+path.Base(file))
	reader, writer := io.Pipe()
	cmd.Stdout = writer
	if err := cmd.Start(); err != nil {
		reader.Close()
		writer.Close()
		return nil, err
	}
	go func() { writer.CloseWithError(cmd.Wait()) }()
	return reader, nil
}
func (i *tartInstance) Stop(ctx context.Context, grace time.Duration) error {
	return tartCommand(ctx, "stop", i.id).Run()
}
func (i *tartInstance) Destroy(ctx context.Context) error { return i.runtime.DestroyByID(ctx, i.id) }
