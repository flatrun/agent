package build

import (
	"context"
	"os"
	"path/filepath"
)

const KindDockerfile Kind = "dockerfile"

// Dockerfile builds a tree with the Dockerfile at its root.
type Dockerfile struct {
	Run Runner
}

func (Dockerfile) Name() Kind { return KindDockerfile }

func (Dockerfile) Detect(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "Dockerfile"))
	return err == nil && !info.IsDir()
}

func (d Dockerfile) Build(ctx context.Context, req Request, sink func(string)) error {
	sink("Building " + req.Tag + " from Dockerfile")
	return d.Run(ctx, "docker", []string{"build", "-t", req.Tag, req.Dir}, sink)
}
