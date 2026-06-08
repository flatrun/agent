package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// ExtractFileFromImage reads a single file from a container image without
// running it, by creating a stopped container and copying the file out.
// Missing images are pulled, so the call is bounded by a timeout.
func ExtractFileFromImage(image, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	createOut, err := exec.CommandContext(ctx, "docker", "create", image).Output()
	if err != nil {
		return nil, fmt.Errorf("create container from %s: %w", image, err)
	}
	containerID := strings.TrimSpace(string(createOut))
	defer exec.Command("docker", "rm", "-f", containerID).Run()

	cpOut, err := exec.CommandContext(ctx, "docker", "cp", containerID+":"+path, "-").Output()
	if err != nil {
		return nil, fmt.Errorf("copy %s from %s: %w", path, image, err)
	}

	// docker cp to stdout emits a tar stream containing the requested file.
	reader := tar.NewReader(bytes.NewReader(cpOut))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar from %s: %w", image, err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			return nil, fmt.Errorf("read %s from %s: %w", path, image, err)
		}
		return content, nil
	}

	return nil, fmt.Errorf("no regular file at %s in %s", path, image)
}
