package runtimes

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/flatrun/agent/internal/guestinit"
	"golang.org/x/sys/unix"
)

func (f *Firecracker) exporter() *Docker { return &Docker{cli: f.docker.cli, Scope: f.Scope} }

func (f *Firecracker) prepareRoot(ctx context.Context, spec Spec, id string) (string, guestinit.Start, error) {
	command := guestinit.Start{Argv: spec.Command, Cwd: spec.WorkDir}
	if err := f.docker.ensureImage(ctx, spec.Image); err != nil {
		return "", command, err
	}
	image, err := f.docker.cli.ImageInspect(ctx, spec.Image)
	if err != nil {
		return "", command, err
	}
	environment := map[string]string{}
	if image.Config != nil {
		for _, entry := range image.Config.Env {
			key, value, ok := strings.Cut(entry, "=")
			if ok {
				environment[key] = value
			}
		}
		if len(command.Argv) == 0 {
			command.Argv = append(append([]string{}, image.Config.Entrypoint...), image.Config.Cmd...)
		} else {
			command.Argv = append(append([]string{}, image.Config.Entrypoint...), command.Argv...)
		}
		if command.Cwd == "" {
			command.Cwd = image.Config.WorkingDir
		}
	}
	for key, value := range spec.Env {
		environment[key] = value
	}
	command.Env = envList(environment)
	if len(command.Argv) == 0 {
		return "", command, errors.New("image has no command")
	}
	init, err := os.ReadFile(f.Config.GuestInit)
	if err != nil {
		return "", command, err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("root-v2:" + image.ID))
	_, _ = hash.Write(init)
	cache := filepath.Join(f.Config.StateDir, "cache")
	if err = os.MkdirAll(cache, 0700); err != nil {
		return "", command, err
	}
	path := filepath.Join(cache, hex.EncodeToString(hash.Sum(nil))+".ext4")
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", command, err
	}
	defer lock.Close()
	for {
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return "", command, err
		}
		select {
		case <-ctx.Done():
			return "", command, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	if _, err = os.Stat(path); err == nil {
		return path, command, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", command, err
	}
	staging := filepath.Join(f.directory(id), "image")
	if err = os.MkdirAll(staging, 0700); err != nil {
		return "", command, err
	}
	defer os.RemoveAll(staging)
	created, err := f.exporter().createContainer(ctx, &container.Config{Image: image.ID, Cmd: []string{"/flatrun-init"}, Labels: map[string]string{Label: id, ScopeLabel: f.Scope}}, nil, "")
	if err != nil {
		return "", command, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = f.exporter().DestroyByID(cleanup, id)
	}()
	stream, err := f.docker.cli.ContainerExport(ctx, created.ID)
	if err != nil {
		return "", command, err
	}
	root := filepath.Join(staging, "root")
	err = extractRoot(ctx, stream, root)
	_ = stream.Close()
	if err != nil {
		return "", command, err
	}
	confined, err := os.OpenRoot(root)
	if err != nil {
		return "", command, err
	}
	defer confined.Close()
	if err = confined.RemoveAll("flatrun-init"); err != nil {
		return "", command, err
	}
	if err = confined.WriteFile("flatrun-init", init, 0755); err != nil {
		return "", command, err
	}
	if err = confined.MkdirAll("etc", 0755); err != nil {
		return "", command, err
	}
	if err = confined.RemoveAll("etc/resolv.conf"); err != nil {
		return "", command, err
	}
	if err = confined.WriteFile("etc/resolv.conf", []byte("nameserver 1.1.1.1\n"), 0644); err != nil {
		return "", command, err
	}
	size, err := rootImageSize(ctx, root)
	if err != nil {
		return "", command, err
	}
	output := filepath.Join(staging, "root.ext4")
	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return "", command, err
	}
	err = file.Truncate(size)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return "", command, errors.Join(err, closeErr)
	}
	result, err := exec.CommandContext(ctx, "mkfs.ext4", "-q", "-F", "-d", root, output).CombinedOutput()
	if err != nil {
		return "", command, fmt.Errorf("mkfs.ext4: %w: %s", err, result)
	}
	if err = os.Rename(output, path); err != nil {
		return "", command, err
	}
	return path, command, nil
}

func rootImageSize(ctx context.Context, root string) (int64, error) {
	const block = int64(4096)
	type inode struct{ device, number uint64 }
	seen := map[inode]bool{}
	var content int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		stat := info.Sys().(*syscall.Stat_t)
		key := inode{uint64(stat.Dev), stat.Ino}
		if !seen[key] {
			seen[key] = true
			content += ((info.Size()+block-1)/block + 1) * block
		}
		return nil
	})
	return content*2 + (32 << 20), err
}

func extractRoot(ctx context.Context, reader io.Reader, directory string) error {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	tr := tar.NewReader(contextReader{ctx: ctx, reader: reader})
	type link struct{ name, target string }
	links := []link{}
	type directoryMode struct {
		name     string
		mode     os.FileMode
		uid, gid int
	}
	directories := []directoryMode{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if name == "." {
			continue
		}
		if !filepath.IsLocal(name) {
			return fmt.Errorf("invalid export path %q", name)
		}
		if err = root.MkdirAll(filepath.Dir(name), 0755); err != nil {
			return err
		}
		mode := os.FileMode(hdr.Mode & 0777)
		if hdr.Mode&04000 != 0 {
			mode |= os.ModeSetuid
		}
		if hdr.Mode&02000 != 0 {
			mode |= os.ModeSetgid
		}
		if hdr.Mode&01000 != 0 {
			mode |= os.ModeSticky
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = root.MkdirAll(name, 0755)
			directories = append(directories, directoryMode{name, mode, hdr.Uid, hdr.Gid})
		case tar.TypeReg:
			var file *os.File
			file, err = root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err == nil {
				_, err = io.Copy(file, tr)
				if err == nil {
					err = file.Chmod(mode)
				}
				closeErr := file.Close()
				err = errors.Join(err, closeErr)
			}
		case tar.TypeSymlink:
			target := hdr.Linkname
			if filepath.IsAbs(target) {
				target, err = filepath.Rel(filepath.Join("/", filepath.Dir(name)), filepath.Clean(target))
				if err != nil {
					return err
				}
			}
			err = root.Symlink(target, name)
		case tar.TypeLink:
			links = append(links, link{name, hdr.Linkname})
			continue
		case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			continue
		default:
			return fmt.Errorf("unsupported export entry %q", name)
		}
		if err != nil {
			return err
		}
		if os.Geteuid() == 0 && hdr.Typeflag != tar.TypeDir {
			if err = root.Lchown(name, hdr.Uid, hdr.Gid); err != nil {
				return err
			}
		}
		if hdr.Typeflag == tar.TypeReg {
			if err = root.Chmod(name, mode); err != nil {
				return err
			}
		}
	}
	for _, link := range links {
		if err := root.Link(link.target, link.name); err != nil {
			return err
		}
	}
	for index := len(directories) - 1; index >= 0; index-- {
		dir := directories[index]
		if os.Geteuid() == 0 {
			if err = root.Chown(dir.name, dir.uid, dir.gid); err != nil {
				return err
			}
		}
		if err = root.Chmod(dir.name, dir.mode); err != nil {
			return err
		}
	}

	return nil
}

func (f *Firecracker) cachedKernel(ctx context.Context) (string, error) {
	input, err := os.Open(f.Config.Kernel)
	if err != nil {
		return "", err
	}
	defer input.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, contextReader{ctx: ctx, reader: input}); err != nil {
		return "", err
	}
	cache := filepath.Join(f.Config.StateDir, "cache")
	path := filepath.Join(cache, hex.EncodeToString(hash.Sum(nil))+".kernel")
	if err = readableRegularFile(path); err == nil {
		return path, nil
	}
	temp, err := os.CreateTemp(cache, "kernel-")
	if err != nil {
		return "", err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if _, err = input.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if _, err = io.Copy(temp, contextReader{ctx: ctx, reader: input}); err != nil {
		return "", err
	}
	if err = temp.Chmod(0444); err != nil {
		return "", err
	}
	if err = temp.Sync(); err != nil {
		return "", err
	}
	if err = os.Rename(temp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}
