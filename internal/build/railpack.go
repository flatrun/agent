package build

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
)

const KindRailpack Kind = "railpack"

// RailpackFrontend is the BuildKit frontend image that executes a Railpack plan.
const RailpackFrontend = "ghcr.io/railwayapp/railpack-frontend"

var ErrRailpackMissing = errors.New("railpack is not installed on this server; install it with the FlatRun installer or from https://railpack.com to build repositories without a Dockerfile")

// Railpack builds any tree by detecting its language and framework, so it
// accepts every directory and belongs last in a registry.
type Railpack struct {
	Run      Runner
	LookPath func(string) (string, error)
}

func (Railpack) Name() Kind { return KindRailpack }

func (Railpack) Detect(string) bool { return true }

func (r Railpack) Build(ctx context.Context, req Request, sink func(string)) error {
	if _, err := r.LookPath("railpack"); err != nil {
		return ErrRailpackMissing
	}
	plan := filepath.Join(req.Scratch, "railpack-plan.json")
	info := filepath.Join(req.Scratch, "railpack-info.json")
	sink("Detecting build plan with Railpack")
	if err := r.Run(ctx, "railpack", []string{"prepare", req.Dir, "--plan-out", plan, "--info-out", info}, sink); err != nil {
		return fmt.Errorf("railpack could not plan this build: %w", err)
	}
	sink("Building " + req.Tag + " with Railpack")
	return r.Run(ctx, "docker", []string{
		"buildx", "build",
		"--build-arg", "BUILDKIT_SYNTAX=" + RailpackFrontend,
		"-f", plan,
		"--load",
		"-t", req.Tag,
		req.Dir,
	}, sink)
}
