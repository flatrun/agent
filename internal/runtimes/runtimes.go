// Package runtimes creates, runs and destroys isolated instances. It knows
// nothing about services, runs, routing or schedules: those lifecycles are
// built on top of it.
package runtimes

import (
	"context"
	"errors"
	"io"
	"time"
)

type Kind string

const (
	KindDocker      Kind = "docker"
	KindFirecracker Kind = "firecracker"
)

// ErrUnavailable wraps the reason a runtime cannot run anything on this node.
var ErrUnavailable = errors.New("runtime unavailable")

type Resources struct {
	CPUs   float64 `json:"cpus,omitempty"`
	Memory uint64  `json:"memory,omitempty"`
}

// Network is denied unless Egress is set. Instances never share a network
// with each other or with deployments.
type Network struct {
	Egress bool `json:"egress,omitempty"`
}

// Spec describes one instance. Nothing from the host enters it: there are
// no mounts, so inputs arrive through the image, the command or the env.
type Spec struct {
	Image     string            `json:"image"`
	Command   []string          `json:"command,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	WorkDir   string            `json:"work_dir,omitempty"`
	Resources Resources         `json:"resources"`
	Network   Network           `json:"network"`
	Labels    map[string]string `json:"labels,omitempty"`
}

type Exit struct {
	Code int `json:"code"`
}

type Runtime interface {
	Kind() Kind
	// Available returns nil when the runtime can run instances here, or an
	// error wrapping ErrUnavailable that says why not.
	Available(ctx context.Context) error
	Create(ctx context.Context, spec Spec) (Instance, error)
}

type Instance interface {
	ID() string
	Start(ctx context.Context) error
	Wait(ctx context.Context) (Exit, error)
	// Logs returns stdout and stderr interleaved as plain text.
	Logs(ctx context.Context, follow bool) (io.ReadCloser, error)
	// CopyOut returns a tar stream of path inside the instance.
	CopyOut(ctx context.Context, path string) (io.ReadCloser, error)
	Stop(ctx context.Context, grace time.Duration) error
	// Destroy removes every resource the instance created. It is safe to call
	// more than once and on an instance that never started.
	Destroy(ctx context.Context) error
}

// Label marks every resource a runtime creates, so leftovers can be found
// after a crash.
const Label = "dev.flatrun.runtime.instance"

const ScopeLabel = "dev.flatrun.runtime.scope"

type LeftoverCleaner interface {
	Leftovers(context.Context) ([]string, error)
	DestroyByID(context.Context, string) error
}
