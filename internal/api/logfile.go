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

func resolveLogSource(meta *models.ServiceMetadata, id string) (models.LogSource, bool) {
	if meta == nil {
		if id == "" || id == models.LogSourceStdout {
			return models.LogSource{ID: models.LogSourceStdout, Name: "Container output", Type: models.LogSourceStdout}, true
		}
		return models.LogSource{}, false
	}
	return meta.FindLogSource(id)
}

// resolveLogServices turns the requested service into the compose service list to read logs
// from: none for "" or "all", meaning every service. The name is checked against the compose
// file rather than passed through, so a caller cannot smuggle arguments into compose.
func (s *Server) resolveLogServices(name, service string) ([]string, error) {
	if service == "" || service == "all" {
		return nil, nil
	}
	names, err := s.manager.GetComposeServiceNames(name)
	if err != nil {
		return nil, err
	}
	for _, sn := range names {
		if sn == service {
			return []string{service}, nil
		}
	}
	return nil, fmt.Errorf("service '%s' not found in compose file, available: %s", service, strings.Join(names, ", "))
}

var errLogPathEscapes = errors.New("log source path escapes the deployment directory")

// readAllCap bounds how much a tail<=0 ("all") read pulls into memory.
var readAllCap int64 = 5 << 20 // 5 MiB

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
		// "All" is still bounded: reading a multi-gigabyte log whole would exhaust
		// memory, so cap it to the last readAllCap bytes and drop the partial first
		// line the cap may cut.
		start := int64(0)
		if size > readAllCap {
			start = size - readAllCap
		}
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return "", err
		}
		data, err := io.ReadAll(f)
		if err != nil {
			return "", err
		}
		s := string(data)
		if start > 0 {
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = s[i+1:]
			}
		}
		return s, nil
	}

	const chunk = 32 * 1024
	var (
		buf       []byte
		offset    = size
		lineCount int
	)
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

func fileLogReadError(err error) error {
	return fmt.Errorf("could not read log file: %w", err)
}
