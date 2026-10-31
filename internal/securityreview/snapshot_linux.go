package securityreview

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/flatrun/agent/pkg/securityprofile"
	"golang.org/x/sys/unix"
)

type Snapshot struct {
	Path       string
	Omissions  []Omission
	Boundaries []Omission
	Files      int
	Bytes      int64
}

func SnapshotFiles(scratch, root string, paths []string, budget securityprofile.Budget) (Snapshot, error) {
	snapshot := Snapshot{Omissions: []Omission{}, Boundaries: []Omission{}}
	if budget.MaxFiles < 1 || budget.MaxSnapshotBytes < 1 {
		return snapshot, fmt.Errorf("snapshot budget required")
	}
	fd, err := openDirectory(root)
	if err != nil {
		return snapshot, fmt.Errorf("snapshot root unavailable or crosses symbolic link")
	}
	defer syscall.Close(fd)
	dir, err := os.MkdirTemp(scratch, "snapshot-")
	if err != nil {
		return snapshot, err
	}
	if err = os.Chmod(dir, 0755); err != nil {
		return snapshot, err
	}
	snapshot.Path = dir
	visited := map[string]bool{}
	nodes := 0
	var walk func(int, string, string) error
	walk = func(parent int, name, relative string) error {
		nodes++
		if nodes > budget.MaxFiles {
			snapshot.Omissions = append(snapshot.Omissions, Omission{relative, "entry budget reached"})
			return nil
		}
		if visited[relative] {
			return nil
		}
		visited[relative] = true
		handle, e := unix.Openat(parent, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e != nil {
			snapshot.Omissions = append(snapshot.Omissions, Omission{relative, "unreadable path"})
			return nil
		}
		defer unix.Close(handle)
		var metadata unix.Stat_t
		if e = unix.Fstat(handle, &metadata); e != nil {
			snapshot.Omissions = append(snapshot.Omissions, Omission{relative, "unreadable metadata"})
			return nil
		}
		if metadata.Mode&unix.S_IFMT != unix.S_IFREG && metadata.Mode&unix.S_IFMT != unix.S_IFDIR {
			snapshot.Boundaries = append(snapshot.Boundaries, Omission{relative, "symbolic link or unsupported file type"})
			return nil
		}
		child, e := syscall.Open(fmt.Sprintf("/proc/self/fd/%d", handle), syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
		if e != nil {
			snapshot.Omissions = append(snapshot.Omissions, Omission{relative, "unreadable or symbolic link boundary"})
			return nil
		}
		file := os.NewFile(uintptr(child), name)
		defer file.Close()
		info, e := file.Stat()
		if e != nil {
			snapshot.Omissions = append(snapshot.Omissions, Omission{relative, "unreadable metadata"})
			return nil
		}
		if info.IsDir() {
			if e = os.MkdirAll(filepath.Join(dir, relative), 0755); e != nil {
				return e
			}
			for {
				entries, readErr := file.ReadDir(128)
				for _, entry := range entries {
					if entry.Type()&os.ModeSymlink != 0 {
						snapshot.Boundaries = append(snapshot.Boundaries, Omission{filepath.Join(relative, entry.Name()), "symbolic link not followed"})
						continue
					}
					if e = walk(child, entry.Name(), filepath.Join(relative, entry.Name())); e != nil {
						return e
					}
					if snapshot.Files >= budget.MaxFiles {
						snapshot.Omissions = append(snapshot.Omissions, Omission{relative, "file budget reached"})
						return nil
					}
				}
				if readErr == io.EOF {
					break
				}
				if readErr != nil {
					snapshot.Omissions = append(snapshot.Omissions, Omission{relative, "directory unreadable"})
					break
				}
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			snapshot.Boundaries = append(snapshot.Boundaries, Omission{relative, "unsupported file type"})
			return nil
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 {
			snapshot.Boundaries = append(snapshot.Boundaries, Omission{relative, "hardlink or unknown link count"})
			return nil
		}
		if snapshot.Files >= budget.MaxFiles || info.Size() > budget.MaxFileBytes || snapshot.Bytes+info.Size() > budget.MaxSnapshotBytes {
			snapshot.Omissions = append(snapshot.Omissions, Omission{relative, "file or byte budget reached"})
			return nil
		}
		if e = os.MkdirAll(filepath.Dir(filepath.Join(dir, relative)), 0755); e != nil {
			return e
		}
		out, e := os.OpenFile(filepath.Join(dir, relative), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0444)
		if e != nil {
			return e
		}
		n, copyErr := io.Copy(out, io.LimitReader(file, info.Size()+1))
		closeErr := out.Close()
		after, statErr := file.Stat()
		if copyErr != nil || closeErr != nil || statErr != nil || n != info.Size() || after.Size() != info.Size() || after.ModTime() != info.ModTime() {
			snapshot.Omissions = append(snapshot.Omissions, Omission{relative, "file changed or read incomplete"})
		}
		snapshot.Files++
		snapshot.Bytes += n
		return nil
	}
	for _, p := range paths {
		if p == "" || filepath.IsAbs(p) || filepath.Clean(p) != p || p == ".." || startsParent(p) {
			return snapshot, fmt.Errorf("invalid snapshot path")
		}
		parts := strings.Split(p, string(filepath.Separator))
		parent := fd
		var opened []int
		for _, part := range parts[:len(parts)-1] {
			next, e := syscall.Openat(parent, part, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
			if e != nil {
				snapshot.Omissions = append(snapshot.Omissions, Omission{p, "parent directory unavailable or symbolic link"})
				parent = -1
				break
			}
			opened = append(opened, next)
			parent = next
		}
		if parent >= 0 {
			err = walk(parent, parts[len(parts)-1], p)
		}
		for _, handle := range opened {
			syscall.Close(handle)
		}
		if err != nil {
			return snapshot, err
		}
	}
	return snapshot, nil
}
func openDirectory(root string) (int, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return -1, fmt.Errorf("absolute clean root required")
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(root, "/"), "/") {
		if part == "" {
			continue
		}
		next, e := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		syscall.Close(fd)
		if e != nil {
			return -1, e
		}
		fd = next
	}
	return fd, nil
}
