package guestinit

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const Port = 10000
const DefaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

type Start struct {
	Argv []string `json:"argv"`
	Env  []string `json:"env"`
	Cwd  string   `json:"cwd"`
}

type Server struct {
	mu      sync.Mutex
	output  []byte
	changed chan struct{}
	done    chan struct{}
	cmd     *exec.Cmd
	exited  bool
	code    int
}

func New() *Server { return &Server{changed: make(chan struct{}), done: make(chan struct{})} }

func (s *Server) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.output = append(s.output, p...)
	close(s.changed)
	s.changed = make(chan struct{})
	return len(p), nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/start":
		var request Start
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := s.start(request); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "/logs":
		offset := 0
		for {
			s.mu.Lock()
			data := append([]byte(nil), s.output[offset:]...)
			offset = len(s.output)
			exited, changed := s.exited, s.changed
			s.mu.Unlock()
			if _, err := w.Write(data); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			if exited || r.URL.Query().Get("follow") != "true" {
				return
			}
			select {
			case <-r.Context().Done():
				return
			case <-changed:
			}
		}
	case "/wait":
		select {
		case <-r.Context().Done():
			return
		case <-s.done:
		}
		s.mu.Lock()
		code := s.code
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(struct {
			Code int `json:"code"`
		}{code})
	case "/stop":
		grace, err := time.ParseDuration(r.URL.Query().Get("grace"))
		if err != nil || grace < 0 {
			http.Error(w, "invalid grace", 400)
			return
		}
		s.mu.Lock()
		if s.cmd == nil {
			s.mu.Unlock()
			w.WriteHeader(204)
			return
		}
		pid := s.cmd.Process.Pid
		signalCommand(pid, syscall.SIGTERM)
		s.mu.Unlock()
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-s.done:
		case <-r.Context().Done():
		}
		signalCommand(pid, syscall.SIGKILL)
		w.WriteHeader(204)
	case "/copyout":
		path := r.URL.Query().Get("path")
		if !filepath.IsAbs(path) {
			http.Error(w, "absolute path required", 400)
			return
		}
		if _, err := os.Lstat(path); err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		w.Header().Set("Content-Type", "application/x-tar")
		tw := tar.NewWriter(w)
		defer tw.Close()
		err := filepath.Walk(path, func(name string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			link := ""
			if info.Mode()&os.ModeSymlink != 0 {
				var err error
				link, err = os.Readlink(name)
				if err != nil {
					return err
				}
			}
			hdr, err := tar.FileInfoHeader(info, link)
			if err != nil {
				return err
			}
			hdr.Name, err = filepath.Rel(filepath.Dir(path), name)
			if err != nil {
				return err
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			file, err := os.Open(name)
			if err != nil {
				return err
			}
			defer file.Close()
			_, err = io.Copy(tw, file)
			return err
		})
		if err != nil {
			panic(http.ErrAbortHandler)
		}
	case "/shutdown":
		if os.Getpid() != 1 {
			http.Error(w, "requires PID 1", 403)
			return
		}
		shutdown()
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) start(request Start) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil {
		return errors.New("command already started")
	}
	if len(request.Argv) == 0 {
		return errors.New("argv is required")
	}
	if request.Cwd != "" {
		if !filepath.IsAbs(request.Cwd) {
			return errors.New("cwd must be absolute")
		}
		if err := os.MkdirAll(request.Cwd, 0755); err != nil {
			return err
		}
	}
	path := DefaultPath
	for _, entry := range request.Env {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			path = value
		}
	}
	executable := request.Argv[0]
	if !strings.ContainsRune(executable, '/') {
		executable = ""
		for _, dir := range filepath.SplitList(path) {
			candidate := filepath.Join(dir, request.Argv[0])
			if !filepath.IsAbs(candidate) {
				candidate = filepath.Join(request.Cwd, candidate)
			}
			if stat, err := os.Stat(candidate); err == nil && stat.Mode().IsRegular() && stat.Mode()&0111 != 0 {
				var err error
				executable, err = filepath.Abs(candidate)
				if err != nil {
					return err
				}
				break
			}
		}
		if executable == "" {
			return fmt.Errorf("executable %q not found in PATH", request.Argv[0])
		}
	}
	cmd := exec.Command(executable, request.Argv[1:]...)
	cmd.Env = append([]string{"PATH=" + DefaultPath}, request.Env...)
	cmd.Dir = request.Cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = writer, writer
	if err := cmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return err
	}
	_ = writer.Close()
	s.cmd = cmd
	drained := make(chan struct{})
	go func() { _, _ = io.Copy(s, reader); _ = reader.Close(); close(drained) }()
	go func() {
		err := cmd.Wait()
		signalCommand(cmd.Process.Pid, syscall.SIGKILL)
		<-drained
		code := 0
		if err != nil {
			code = cmd.ProcessState.ExitCode()
			if code < 0 {
				if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
					code = 128 + int(status.Signal())
				}
			}
		}
		s.mu.Lock()
		s.code = code
		s.exited = true
		close(s.done)
		close(s.changed)
		s.changed = make(chan struct{})
		s.mu.Unlock()
	}()
	return nil
}

func (s *Server) Reap() {
	for {
		time.Sleep(100 * time.Millisecond)
		entries, err := os.ReadDir("/proc")
		if err != nil {
			continue
		}
		s.mu.Lock()
		for _, entry := range entries {
			var pid int
			if _, err := fmt.Sscanf(entry.Name(), "%d", &pid); err != nil || (s.cmd != nil && pid == s.cmd.Process.Pid) {
				continue
			}
			data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "status"))
			if err != nil {
				continue
			}
			if strings.Contains(string(data), fmt.Sprintf("PPid:\t%d\n", os.Getpid())) {
				var status syscall.WaitStatus
				_, _ = syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
			}
		}
		s.mu.Unlock()
	}
}

func signalCommand(pid int, signal syscall.Signal) {
	_ = syscall.Kill(-pid, signal)
	if os.Getpid() == 1 {
		_ = syscall.Kill(-1, signal)
	}
}
