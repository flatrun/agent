// Package build turns a source tree without a compose file into a container
// image: with its own Dockerfile when it has one, otherwise with Railpack, which
// detects the language and builds without any configuration.
package build

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/flatrun/agent/internal/docker"
)

type Kind string

const (
	KindCompose    Kind = "compose"
	KindDockerfile Kind = "dockerfile"
	KindRailpack   Kind = "railpack"
)

// RailpackFrontend is the BuildKit frontend image that executes a Railpack plan.
const RailpackFrontend = "ghcr.io/railwayapp/railpack-frontend"

var ErrRailpackMissing = errors.New("railpack is not installed on this server; install it with the FlatRun installer or from https://railpack.com to build repositories without a Dockerfile")

// Detect picks how a source tree is run: its compose file, then its Dockerfile,
// then Railpack.
func Detect(dir string) Kind {
	if docker.FindComposeFile(dir) != "" {
		return KindCompose
	}
	if info, err := os.Stat(filepath.Join(dir, "Dockerfile")); err == nil && !info.IsDir() {
		return KindDockerfile
	}
	return KindRailpack
}

// Runner executes one command, streaming its combined output lines to sink.
type Runner func(ctx context.Context, name string, args []string, sink func(string)) error

type Builder struct {
	Run      Runner
	LookPath func(string) (string, error)
}

func New() *Builder {
	return &Builder{Run: runCommand, LookPath: exec.LookPath}
}

// Build produces image tag from dir. scratch holds the Railpack plan files and
// must not be inside dir, so the plan never becomes part of the build context.
func (b *Builder) Build(ctx context.Context, kind Kind, dir, scratch, tag string, sink func(string)) error {
	switch kind {
	case KindDockerfile:
		sink("Building " + tag + " from Dockerfile")
		return b.Run(ctx, "docker", []string{"build", "-t", tag, dir}, sink)
	case KindRailpack:
		if _, err := b.LookPath("railpack"); err != nil {
			return ErrRailpackMissing
		}
		plan := filepath.Join(scratch, "railpack-plan.json")
		info := filepath.Join(scratch, "railpack-info.json")
		sink("Detecting build plan with Railpack")
		if err := b.Run(ctx, "railpack", []string{"prepare", dir, "--plan-out", plan, "--info-out", info}, sink); err != nil {
			return fmt.Errorf("railpack could not plan this build: %w", err)
		}
		sink("Building " + tag + " with Railpack")
		return b.Run(ctx, "docker", []string{
			"buildx", "build",
			"--build-arg", "BUILDKIT_SYNTAX=" + RailpackFrontend,
			"-f", plan,
			"--load",
			"-t", tag,
			dir,
		}, sink)
	default:
		return fmt.Errorf("nothing to build for %q sources", kind)
	}
}

// ImageTag names the image built for a deployment at a revision.
func ImageTag(deployment, revision string) string {
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if revision == "" {
		revision = "latest"
	}
	return "flatrun/" + strings.ToLower(deployment) + ":" + revision
}

func runCommand(ctx context.Context, name string, args []string, sink func(string)) error {
	cmd := exec.CommandContext(ctx, name, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var tail []string
	forward := func(r io.Reader) {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			sink(line)
			mu.Lock()
			tail = append(tail, line)
			if len(tail) > 5 {
				tail = tail[1:]
			}
			mu.Unlock()
		}
	}
	wg.Add(2)
	go forward(stdout)
	go forward(stderr)
	wg.Wait()
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%s %s failed: %w: %s", name, args[0], err, strings.Join(tail, " | "))
	}
	return nil
}
