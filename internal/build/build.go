// Package build turns a source tree without a compose file into a container
// image. Each way of building is a Builder; a Registry tries them in order and
// the first that recognizes the tree builds it. Adding or replacing a builder
// is a registration, not a change to the deploy flow.
package build

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/flatrun/agent/internal/docker"
)

// Kind names how a source runs: KindCompose for a tree that brings its own
// compose file, otherwise the Name of the builder that builds it.
type Kind string

const KindCompose Kind = "compose"

// Request is one build: the source tree, a scratch directory outside it for
// builder-generated files, and the image tag to produce.
type Request struct {
	Dir     string
	Scratch string
	Tag     string
}

type Builder interface {
	Name() Kind
	// Detect reports whether this builder can build the tree in dir.
	Detect(dir string) bool
	Build(ctx context.Context, req Request, sink func(string)) error
}

// Registry holds builders in priority order.
type Registry struct {
	builders []Builder
}

func NewRegistry(builders ...Builder) *Registry {
	return &Registry{builders: builders}
}

// DefaultRegistry builds with the repository's own Dockerfile when it has one
// and falls back to Railpack's zero-config detection.
func DefaultRegistry() *Registry {
	run := Runner(runCommand)
	return NewRegistry(
		Dockerfile{Run: run},
		Railpack{Run: run, LookPath: exec.LookPath},
	)
}

// Detect picks how a source tree runs: its compose file, else the first
// builder that recognizes it. It returns "" when nothing can build it.
func (r *Registry) Detect(dir string) Kind {
	if docker.FindComposeFile(dir) != "" {
		return KindCompose
	}
	for _, b := range r.builders {
		if b.Detect(dir) {
			return b.Name()
		}
	}
	return ""
}

func (r *Registry) Get(kind Kind) (Builder, bool) {
	for _, b := range r.builders {
		if b.Name() == kind {
			return b, true
		}
	}
	return nil, false
}

func (r *Registry) Build(ctx context.Context, kind Kind, req Request, sink func(string)) error {
	b, ok := r.Get(kind)
	if !ok {
		return fmt.Errorf("no builder registered for %q", kind)
	}
	return b.Build(ctx, req, sink)
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

const maxOutputLine = 1024 * 1024

// splitLongLines splits on newlines and cuts a line longer than the scanner's
// buffer into pieces instead of failing the scan.
func splitLongLines(data []byte, atEOF bool) (int, []byte, error) {
	advance, token, err := bufio.ScanLines(data, atEOF)
	if advance == 0 && token == nil && err == nil && len(data) >= maxOutputLine {
		return maxOutputLine, data[:maxOutputLine], nil
	}
	return advance, token, err
}

// Runner executes one command, streaming its combined output lines to sink.
type Runner func(ctx context.Context, name string, args []string, sink func(string)) error

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
		scanner.Buffer(make([]byte, 64*1024), maxOutputLine)
		scanner.Split(splitLongLines)
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
		// The command blocks once a pipe it writes to is full, so whatever the
		// scanner could not read is drained or the build never exits.
		_, _ = io.Copy(io.Discard, r)
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
