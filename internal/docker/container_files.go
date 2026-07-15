package docker

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// ContainerFile is one entry of a directory inside a container.
type ContainerFile struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	Mode        string `json:"mode"`
	IsDir       bool   `json:"is_dir"`
	IsSymlink   bool   `json:"is_symlink"`
	LinkTarget  string `json:"link_target,omitempty"`
	ModifiedRaw string `json:"modified_raw,omitempty"`
}

// ListContainerPath lists a directory inside a running container.
//
// The Engine API can stat a path and archive it, but cannot list a directory,
// so this asks the container itself. That means an image with no shell, such as
// a distroless or scratch build, cannot be browsed; its paths can still be
// copied out by naming them directly.
func (a *APIClient) ListContainerPath(ctx context.Context, containerID, dir string) ([]ContainerFile, error) {
	if dir == "" {
		dir = "/"
	}

	// dir reaches a shell, so it is quoted rather than interpolated: a path of
	// "/etc; rm -rf /" would otherwise run.
	out, err := a.ExecInContainer(ctx, containerID, "ls -lA -- "+shellQuote(dir))
	if err != nil {
		return nil, fmt.Errorf("failed to list %s: %w", dir, err)
	}

	return parseLSOutput(out, dir), nil
}

// parseLSOutput reads `ls -lA`, whose columns are the same under both busybox
// and GNU coreutils and differ only in padding:
//
//	drwxr-xr-x 2 root root 4096 Jul 14 01:22 conf.d
func parseLSOutput(out, dir string) []ContainerFile {
	var files []ContainerFile

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "total ") {
			continue
		}

		name, rest, ok := splitLSFields(line)
		if !ok {
			continue
		}

		file := ContainerFile{
			Mode:        rest.mode,
			Size:        rest.size,
			ModifiedRaw: rest.modified,
			IsDir:       strings.HasPrefix(rest.mode, "d"),
			IsSymlink:   strings.HasPrefix(rest.mode, "l"),
		}

		// A symlink is listed as "link -> target".
		if file.IsSymlink {
			if i := strings.Index(name, " -> "); i >= 0 {
				file.LinkTarget = name[i+4:]
				name = name[:i]
			}
		}

		file.Name = name
		file.Path = path.Join(dir, name)
		files = append(files, file)
	}

	return files
}

type lsFields struct {
	mode     string
	size     int64
	modified string
}

// splitLSFields pulls the fixed columns off an `ls -l` line and returns the rest
// as the name, which is everything after the timestamp so that names containing
// spaces survive.
func splitLSFields(line string) (name string, fields lsFields, ok bool) {
	const columnsBeforeName = 8

	rest := line
	var columns []string
	for i := 0; i < columnsBeforeName; i++ {
		rest = strings.TrimLeft(rest, " \t")
		cut := strings.IndexAny(rest, " \t")
		if cut < 0 {
			return "", lsFields{}, false
		}
		columns = append(columns, rest[:cut])
		rest = rest[cut:]
	}

	name = strings.TrimLeft(rest, " \t")
	if name == "" {
		return "", lsFields{}, false
	}

	size, _ := strconv.ParseInt(columns[4], 10, 64)
	return name, lsFields{
		mode:     columns[0],
		size:     size,
		modified: strings.Join(columns[5:8], " "),
	}, true
}

// shellQuote wraps a value in single quotes so a shell treats it as one literal
// argument, escaping any single quote it contains.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// ListServicePath lists a directory inside a compose service's running container.
func (a *APIClient) ListServicePath(ctx context.Context, project, service, dir string) ([]ContainerFile, error) {
	containerID, err := a.FindContainer(ctx, project, service)
	if err != nil {
		return nil, err
	}
	return a.ListContainerPath(ctx, containerID, dir)
}

// ComposeProject returns the project name a deployment's containers are labelled
// with, which is what addresses them on the engine.
func (m *Manager) ComposeProject(name string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	deployment, err := m.discovery.GetDeployment(name)
	if err != nil {
		return "", err
	}
	return m.projectFor(deployment, containerIndex{}), nil
}

// ListServiceFiles lists a directory inside a running service's container.
func (m *Manager) ListServiceFiles(ctx context.Context, project, service, dir string) ([]ContainerFile, error) {
	if m.apiClient == nil {
		return nil, fmt.Errorf("docker api client not available")
	}
	return m.apiClient.ListServicePath(ctx, project, service, dir)
}
