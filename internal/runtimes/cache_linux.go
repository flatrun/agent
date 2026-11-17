package runtimes

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func (i *firecrackerInstance) attachCache(ctx context.Context) error {
	directory := filepath.Join(i.runtime.base(), "caches", cacheName(i.runtime.Scope, i.spec.CacheKey))
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	for slot := 0; slot < 64; slot++ {
		path := filepath.Join(directory, fmt.Sprintf("%d.ext4", slot))
		lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			lock.Close()
			if errors.Is(err, unix.EWOULDBLOCK) {
				continue
			}
			return err
		}

		if info, err := os.Stat(path); err == nil {
			if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
				_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
				_ = lock.Close()
				continue
			}
		}
		i.cacheLock = lock
		if _, err = os.Stat(path); errors.Is(err, os.ErrNotExist) {
			file, err := os.CreateTemp(directory, "cache-*.tmp")
			if err != nil {
				return err
			}
			temp := file.Name()
			defer os.Remove(temp)
			err = file.Truncate(10 << 30)
			closeErr := file.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			if output, err := exec.CommandContext(ctx, "mkfs.ext4", "-F", "-q", temp).CombinedOutput(); err != nil {
				return fmt.Errorf("format cache: %w: %s", err, output)
			}
			if err = os.Rename(temp, path); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}

		if err := os.Chown(path, 65534, 65534); err != nil {
			return err
		}
		return os.Link(path, filepath.Join(i.jail(), "cache.ext4"))
	}
	return errors.New("all 64 cache disks are in use")
}
