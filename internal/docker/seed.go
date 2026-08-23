package docker

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// materializeTimeout bounds the copy out of a container. A path holding an
// application's whole data directory can be large, so this is generous.
const materializeTimeout = 10 * time.Minute

// MaterializeMount copies a path out of a running service's container onto the
// host, then mounts the host copy back at the same place and brings the service
// up again.
//
// The order matters. A bind mount pushes the host's content into the container,
// so mounting a path the container populated would hide it. Copying first means
// the service resumes on exactly the content it was already running, now visible
// and editable on the host. The service is stopped before the copy so no write
// can land between the copy and the mount taking effect and be lost.
func (m *Manager) MaterializeMount(name, service, containerPath, hostPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.apiClient == nil {
		return fmt.Errorf("docker api client not available")
	}

	deployment, err := m.discovery.GetDeployment(name)
	if err != nil {
		return err
	}

	fullPath, err := resolveMountHostPath(deployment.Path, hostPath)
	if err != nil {
		return err
	}
	hostPath = normalizeMountHostPath(hostPath)

	content, filename, err := m.discovery.GetComposeFile(name)
	if err != nil {
		return err
	}

	mount := hostPath + ":" + containerPath
	if HasVolumeMount(content, service, mount) {
		return fmt.Errorf("%s is already mounted at %s", containerPath, hostPath)
	}

	// Refuse rather than merge: the host copy has to be the container's content
	// alone, or the service would resume on something it never had.
	seedable, err := isSeedable(fullPath)
	if err != nil {
		return err
	}
	if !seedable {
		return fmt.Errorf("%s already has content on the host", hostPath)
	}

	ctx, cancel := context.WithTimeout(context.Background(), materializeTimeout)
	defer cancel()

	project := m.projectFor(deployment, containerIndex{})
	containerID, err := m.apiClient.FindContainer(ctx, project, service)
	if err != nil {
		return fmt.Errorf("service %s must be running to copy %s from it: %w", service, containerPath, err)
	}

	if out, err := m.executor.StopService(deployment.Path, service); err != nil {
		return fmt.Errorf("failed to stop %s before copying: %w (%s)", service, err, out)
	}

	if err := m.apiClient.CopyPathToHost(ctx, containerID, containerPath, fullPath); err != nil {
		// Leave the service as it was found rather than stopped on a failure
		// that has changed nothing else.
		if _, upErr := m.executor.Up(deployment.Path); upErr != nil {
			log.Printf("mounts: failed to restart %s after a failed copy: %v", name, upErr)
		}
		return err
	}

	updated, err := AddVolumeToService(content, service, mount)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(deployment.Path, filename), []byte(updated), 0644); err != nil {
		return err
	}

	if out, err := m.executor.Up(deployment.Path, WithForceRecreate()); err != nil {
		return fmt.Errorf("failed to bring %s back up on the mounted copy: %w (%s)", name, err, out)
	}
	return nil
}

// UnmountPath removes a bind mount from a service and recreates it, so the
// service goes back to whatever its image holds at that path.
//
// The recreate is what makes the unmount real. Dropping the mount from the
// compose file alone leaves the running container still bound to the host, so
// the host copy would keep reaching into it.
//
// The host copy is left on disk. It is the only place the mounted content
// exists, and the service no longer reads it, so deleting it is a separate
// decision for the caller to make deliberately.
func (m *Manager) UnmountPath(name, service, hostPath, containerPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	deployment, err := m.discovery.GetDeployment(name)
	if err != nil {
		return err
	}

	content, filename, err := m.discovery.GetComposeFile(name)
	if err != nil {
		return err
	}

	hostPath = normalizeMountHostPath(hostPath)
	volume, found := FindVolumeMount(content, service, hostPath, containerPath)
	if !found {
		return fmt.Errorf("%s is not mounted at %s in %s", hostPath, containerPath, service)
	}

	updated, err := RemoveVolumeFromService(content, service, volume)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(deployment.Path, filename), []byte(updated), 0644); err != nil {
		return err
	}

	if out, err := m.executor.Up(deployment.Path, WithForceRecreate()); err != nil {
		return fmt.Errorf("failed to recreate %s without the mount: %w (%s)", name, err, out)
	}
	return nil
}

// SeedMounts fills the named bind mounts of a deployment that has not started
// yet with the content its images hold at those paths.
//
// It suits a deployment with no container to copy from. An image holds nothing
// an entrypoint generates on first start, so an already-running deployment is
// better served by copying the container itself.
//
// Only mounts named by the caller are seeded, and only where the host side is
// still missing or empty, so seeding never overwrites content a user has.
func (m *Manager) SeedMounts(name string, hostPaths []string) error {
	if len(hostPaths) == 0 {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.apiClient == nil {
		return fmt.Errorf("docker api client not available")
	}

	deployment, err := m.discovery.GetDeployment(name)
	if err != nil {
		return err
	}

	content, _, err := m.discovery.GetComposeFile(name)
	if err != nil {
		return err
	}

	var compose composeFile
	if err := yaml.Unmarshal([]byte(content), &compose); err != nil {
		return fmt.Errorf("failed to read the compose file: %w", err)
	}

	wanted := make(map[string]bool, len(hostPaths))
	for _, p := range hostPaths {
		wanted[normalizeMountHostPath(p)] = true
	}

	ctx, cancel := context.WithTimeout(context.Background(), materializeTimeout)
	defer cancel()

	var failures []string
	for _, service := range compose.Services {
		if service.Image == "" {
			continue
		}
		for _, volume := range service.Volumes {
			hostPath, containerPath := volume.Source, volume.Target
			if hostPath == "" || containerPath == "" || !wanted[normalizeMountHostPath(hostPath)] {
				continue
			}

			fullPath, err := resolveMountHostPath(deployment.Path, hostPath)
			if err != nil {
				return err
			}

			seedable, err := isSeedable(fullPath)
			if err != nil {
				return err
			}
			if !seedable {
				continue
			}

			if err := m.apiClient.SeedFromImage(ctx, service.Image, containerPath, fullPath); err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", hostPath, err))
			}
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("failed to seed %s", strings.Join(failures, "; "))
	}
	return nil
}

// splitBindMount splits a compose volume into its host and container sides,
// ignoring named volumes, which Docker already populates from the image.
func splitBindMount(volume string) (hostPath, containerPath string) {
	parts := strings.Split(volume, ":")
	if len(parts) < 2 {
		return "", ""
	}
	if !strings.HasPrefix(parts[0], ".") && !strings.HasPrefix(parts[0], "/") {
		return "", ""
	}
	return parts[0], parts[1]
}

// resolveMountHostPath turns a mount's host side into an absolute path inside
// the deployment, refusing anything that would land outside it.
//
// These paths arrive from API requests and compose files, so they are untrusted:
// without this, "../../etc" would write a container's content anywhere the agent
// can reach. An absolute path is refused for the same reason, even though compose
// itself allows one.
func resolveMountHostPath(deploymentPath, hostPath string) (string, error) {
	normalized := normalizeMountHostPath(hostPath)
	if normalized == "" {
		return "", fmt.Errorf("a host path is required")
	}
	if filepath.IsAbs(normalized) {
		return "", fmt.Errorf("host path %q must stay inside the deployment directory", hostPath)
	}

	full := filepath.Join(deploymentPath, normalized)
	prefix := filepath.Clean(deploymentPath) + string(os.PathSeparator)
	if !strings.HasPrefix(full, prefix) {
		return "", fmt.Errorf("host path %q must stay inside the deployment directory", hostPath)
	}
	return full, nil
}

// normalizeMountHostPath keeps a host path in the relative form compose files
// use, so a mount reads the same whether the caller passed "nginx" or "./nginx".
func normalizeMountHostPath(hostPath string) string {
	hostPath = strings.TrimSpace(hostPath)
	if hostPath == "" {
		return ""
	}
	if strings.HasPrefix(hostPath, "./") || strings.HasPrefix(hostPath, "../") || filepath.IsAbs(hostPath) {
		return hostPath
	}
	return "./" + hostPath
}

// extractSeedTar writes the tar Docker produces for a copied container path to
// destPath on the host.
//
// Docker roots the archive at the basename of the copied path, so copying
// /etc/nginx/conf.d yields "conf.d/" and "conf.d/default.conf", and copying the
// file /etc/nginx/nginx.conf yields the single entry "nginx.conf". That leading
// component is stripped: destPath is the copied path itself, not its parent.
//
// srcIsDir selects between the two: a file source writes the lone entry to
// destPath, a directory source becomes destPath's contents.
func extractSeedTar(r io.Reader, destPath string, srcIsDir bool) error {
	tr := tar.NewReader(r)

	if !srcIsDir {
		return extractSeedFile(tr, destPath)
	}

	if err := os.MkdirAll(destPath, 0755); err != nil {
		return err
	}

	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		rel := stripRoot(header.Name)
		if rel == "" {
			if err := preserveSeedOwnership(destPath, header.Uid, header.Gid); err != nil {
				return err
			}
			continue
		}

		target, err := resolveSeedPath(destPath, rel)
		if err != nil {
			return err
		}

		if err := writeSeedEntry(tr, header, target); err != nil {
			return err
		}
	}
}

// extractSeedFile writes the first regular entry of a file copy to destPath.
func extractSeedFile(tr *tar.Reader, destPath string) error {
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("archive held no file to seed %s", destPath)
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return err
		}
		if err := writeSeedFile(tr, destPath, header.FileInfo().Mode()); err != nil {
			return err
		}
		return preserveSeedOwnership(destPath, header.Uid, header.Gid)
	}
}

// writeSeedEntry materialises one archive entry beneath an already-resolved
// target path. Entry types other than files, directories and symlinks (devices,
// fifos) are skipped: an image may carry them, but they are not configuration
// worth reproducing on the host.
func writeSeedEntry(tr *tar.Reader, header *tar.Header, target string) error {
	var err error
	switch header.Typeflag {
	case tar.TypeDir:
		err = os.MkdirAll(target, header.FileInfo().Mode().Perm())
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		err = writeSeedFile(tr, target, header.FileInfo().Mode())
	case tar.TypeSymlink:
		// A symlink's target is not followed, so a link pointing outside the
		// deployment only breaks; it cannot be used to write through.
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		_ = os.Remove(target)
		err = os.Symlink(header.Linkname, target)
	default:
		return nil
	}
	if err != nil {
		return err
	}
	return preserveSeedOwnership(target, header.Uid, header.Gid)
}

func preserveSeedOwnership(path string, uid, gid int) error {
	if os.Geteuid() != 0 {
		return nil
	}
	return os.Lchown(path, uid, gid)
}

func writeSeedFile(r io.Reader, path string, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	return f.Close()
}

// stripRoot removes the archive's leading path component, which Docker sets to
// the basename of the copied container path.
func stripRoot(name string) string {
	name = strings.TrimPrefix(filepath.ToSlash(name), "./")
	if i := strings.Index(name, "/"); i >= 0 {
		return strings.Trim(name[i+1:], "/")
	}
	return ""
}

// resolveSeedPath joins an archive entry onto the destination and refuses
// anything that would land outside it. Image content is untrusted input: an
// entry named ../../etc/passwd would otherwise write through the deployment
// directory.
func resolveSeedPath(destPath, rel string) (string, error) {
	target := filepath.Join(destPath, rel)

	prefix := filepath.Clean(destPath) + string(os.PathSeparator)
	if !strings.HasPrefix(target, prefix) {
		return "", fmt.Errorf("archive entry %q escapes %s", rel, destPath)
	}
	return target, nil
}

// isSeedable reports whether a host path may be seeded. Only a missing path or
// an empty directory qualifies, mirroring the copy-on-first-use semantics of
// named volumes, so content a user already has is never overwritten. Empty
// directories count because deployment creation pre-creates mount directories
// before anything is seeded.
func isSeedable(hostPath string) (bool, error) {
	info, err := os.Stat(hostPath)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, nil
	}

	entries, err := os.ReadDir(hostPath)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}
