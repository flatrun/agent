// Package pluginhost launches out-of-process plugin binaries (<pluginsDir>/<name>/plugin),
// each listening on a private unix socket, and reverse-proxies their HTTP routes.
package pluginhost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/flatrun/agent/pkg/pluginapi"
)

type managed struct {
	info   pluginapi.Info
	socket string
	cmd    *exec.Cmd
	proxy  *httputil.ReverseProxy
}

type Host struct {
	pluginsDir string
	runtimeDir string
	agentURL   string
	token      string
	handshake  string

	mu      sync.RWMutex
	running map[string]*managed
}

func New(pluginsDir, runtimeDir, agentURL, token string) *Host {
	return &Host{
		pluginsDir: pluginsDir,
		runtimeDir: runtimeDir,
		agentURL:   agentURL,
		token:      token,
		handshake:  randomCookie(),
		running:    make(map[string]*managed),
	}
}

// Start launches every plugin binary; a plugin that fails to launch is logged and skipped.
func (h *Host) Start() error {
	if h.pluginsDir == "" {
		return nil
	}
	if err := os.MkdirAll(h.runtimeDir, 0755); err != nil {
		return err
	}
	entries, err := os.ReadDir(h.pluginsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		bin := filepath.Join(h.pluginsDir, entry.Name(), "plugin")
		if !isExecutable(bin) {
			continue
		}
		if err := h.launch(entry.Name(), bin); err != nil {
			log.Printf("[pluginhost] %s failed to start: %v", entry.Name(), err)
		}
	}
	return nil
}

func (h *Host) launch(name, bin string) error {
	socket := filepath.Join(h.runtimeDir, name+".sock")
	_ = os.Remove(socket)

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		pluginapi.EnvSocket+"="+socket,
		pluginapi.EnvHandshake+"="+h.handshake,
		pluginapi.EnvAgentURL+"="+h.agentURL,
		pluginapi.EnvToken+"="+h.token,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}

	info, err := h.awaitInfo(socket)
	if err != nil {
		_ = cmd.Process.Kill()
		return err
	}

	h.mu.Lock()
	h.running[name] = &managed{info: info, socket: socket, cmd: cmd, proxy: newUnixProxy(socket)}
	h.mu.Unlock()
	log.Printf("[pluginhost] started %s (%s)", name, info.Version)
	return nil
}

func (h *Host) awaitInfo(socket string) (pluginapi.Info, error) {
	client := &http.Client{Transport: unixTransport(socket), Timeout: 2 * time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for {
		info, err := fetchInfo(client, h.handshake)
		if err == nil {
			return info, nil
		}
		if time.Now().After(deadline) {
			return pluginapi.Info{}, fmt.Errorf("plugin did not report info in time: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func fetchInfo(client *http.Client, handshake string) (pluginapi.Info, error) {
	resp, err := client.Get("http://plugin" + pluginapi.InfoPath)
	if err != nil {
		return pluginapi.Info{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return pluginapi.Info{}, fmt.Errorf("info returned %d", resp.StatusCode)
	}
	if resp.Header.Get(pluginapi.HandshakeHeader) != handshake {
		return pluginapi.Info{}, fmt.Errorf("handshake mismatch")
	}
	var info pluginapi.Info
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return pluginapi.Info{}, err
	}
	return info, nil
}

// Proxy returns a running plugin's reverse proxy. The caller must set the request path
// relative to the plugin (strip the /plugin/<name> prefix) before serving.
func (h *Host) Proxy(name string) (*httputil.ReverseProxy, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	m, ok := h.running[name]
	if !ok {
		return nil, false
	}
	return m.proxy, true
}

func (h *Host) Infos() []pluginapi.Info {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]pluginapi.Info, 0, len(h.running))
	for _, m := range h.running {
		out = append(out, m.info)
	}
	return out
}

func (h *Host) Stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for name, m := range h.running {
		if m.cmd.Process != nil {
			_ = m.cmd.Process.Signal(syscall.SIGTERM)
		}
		_ = os.Remove(m.socket)
		delete(h.running, name)
	}
}

func unixTransport(socket string) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
}

func newUnixProxy(socket string) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = "plugin"
		},
		Transport: unixTransport(socket),
	}
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0111 != 0
}

func randomCookie() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "flatrun-plugin-handshake"
	}
	return hex.EncodeToString(b)
}
