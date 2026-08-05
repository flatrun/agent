package api

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flatrun/agent/pkg/models"
)

// resolveLogSource resolves a source id (from the ?source= query) against a
// deployment's metadata. An empty id, or metadata that has no sources, falls
// back to the container output stream.
func resolveLogSource(meta *models.ServiceMetadata, id string) (models.LogSource, bool) {
	if meta == nil {
		if id == "" || id == models.LogSourceStdout {
			return models.LogSource{ID: models.LogSourceStdout, Name: "Container output", Type: models.LogSourceStdout}, true
		}
		return models.LogSource{}, false
	}
	return meta.FindLogSource(id)
}

// errLogPathEscapes is returned when a configured file log path would resolve
// outside its deployment directory. A log source is only ever allowed to read
// files the deployment owns.
var errLogPathEscapes = errors.New("log source path escapes the deployment directory")

// resolveLogFilePath joins a deployment-relative log path to the deployment
// directory and refuses anything that would climb out of it (via "..", an
// absolute path, or a symlink target outside the tree).
func resolveLogFilePath(deploymentPath, relPath string) (string, error) {
	if relPath == "" {
		return "", errors.New("empty log source path")
	}
	if filepath.IsAbs(relPath) {
		return "", errLogPathEscapes
	}

	base, err := filepath.Abs(deploymentPath)
	if err != nil {
		return "", err
	}
	full := filepath.Clean(filepath.Join(base, relPath))
	if full != base && !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return "", errLogPathEscapes
	}
	return full, nil
}

// readFileTail returns the last n lines of a file, reading from the end so a
// large log does not have to be walked front to back. n <= 0 means all lines.
func readFileTail(path string, n int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := info.Size()
	if size == 0 {
		return "", nil
	}

	if n <= 0 {
		data, err := io.ReadAll(f)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}

	const chunk = 32 * 1024
	var (
		buf       []byte
		offset    = size
		lineCount int
	)
	// Walk backwards a chunk at a time, counting newlines, until we have one
	// more than n (so the final partial line is not cut) or reach the start.
	for offset > 0 {
		readSize := int64(chunk)
		if offset < readSize {
			readSize = offset
		}
		offset -= readSize

		block := make([]byte, readSize)
		if _, err := f.ReadAt(block, offset); err != nil && err != io.EOF {
			return "", err
		}
		buf = append(block, buf...)
		lineCount += bytes.Count(block, []byte{'\n'})
		if lineCount > n {
			break
		}
	}

	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}

// streamFileLogs sends the last tail lines of a file, then follows it, handing
// each newly appended line to sink until ctx is cancelled. It re-opens the file
// if it is rotated (truncated or replaced), which is how log rotation is handled
// without leaving the follower stuck on a stale handle.
func streamFileLogs(ctx context.Context, path string, tail int, sink func(string)) error {
	initial, err := readFileTail(path, tail)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if initial != "" {
		for _, line := range strings.Split(initial, "\n") {
			sink(line)
		}
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// The file may appear later (an app that has not logged yet); wait
			// for it rather than failing the whole stream.
			f = nil
		} else {
			return err
		}
	}
	defer func() {
		if f != nil {
			f.Close()
		}
	}()

	var offset int64
	if f != nil {
		if info, err := f.Stat(); err == nil {
			offset = info.Size()
		}
	}

	reader := func() *bufio.Reader {
		if f == nil {
			return nil
		}
		return bufio.NewReader(f)
	}
	br := reader()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var pending []byte
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		if f == nil {
			opened, err := os.Open(path)
			if err != nil {
				continue
			}
			f = opened
			offset = 0
			br = bufio.NewReader(f)
		}

		info, err := f.Stat()
		if err != nil {
			f.Close()
			f = nil
			continue
		}
		if info.Size() < offset {
			// The file shrank, so it was rotated; start from the top of the new one.
			if _, err := f.Seek(0, io.SeekStart); err == nil {
				offset = 0
				br = bufio.NewReader(f)
				pending = pending[:0]
			}
		}

		for {
			chunk, err := br.ReadBytes('\n')
			if len(chunk) > 0 {
				offset += int64(len(chunk))
				pending = append(pending, chunk...)
				if pending[len(pending)-1] == '\n' {
					sink(strings.TrimRight(string(pending), "\r\n"))
					pending = pending[:0]
				}
			}
			if err != nil {
				break
			}
		}
	}
}

// fileLogReadError wraps an os error so callers get a consistent message for an
// unreadable source instead of a raw filesystem error.
func fileLogReadError(err error) error {
	return fmt.Errorf("could not read log file: %w", err)
}
