// Package pluginhost launches out-of-process plugin binaries (<pluginsDir>/<name>/plugin),
// each listening on a private unix socket, and reverse-proxies their HTTP routes.
package pluginhost

import (
	"bytes"
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

type builtin struct {
	name string
	path string
	args []string
}

type Host struct {
	pluginsDir string
	runtimeDir string
	agentURL   string
	token      string
	dataDir    string
	handshake  string
	builtins   []builtin

	mu      sync.RWMutex
	running map[string]*managed
}

func New(pluginsDir, runtimeDir, agentURL, token string) *Host {
	// pluginsDir is <base>/.flatrun/plugins, so the deployments base is two levels up.
	dataDir := ""
	if pluginsDir != "" {
		dataDir = filepath.Dir(filepath.Dir(pluginsDir))
	}
	return &Host{
		pluginsDir: pluginsDir,
		runtimeDir: runtimeDir,
		agentURL:   agentURL,
		token:      token,
		dataDir:    dataDir,
		handshake:  randomCookie(),
		running:    make(map[string]*managed),
	}
}

// Builtin registers a plugin shipped inside the agent, launched by running the given command
// (typically the agent re-execing itself with a subcommand). Call before Start.
func (h *Host) Builtin(name, path string, args ...string) {
	h.builtins = append(h.builtins, builtin{name: name, path: path, args: args})
}

// Start launches the built-in plugins and every external plugin binary; a plugin that fails
// to launch is logged and skipped.
func (h *Host) Start() error {
	if err := os.MkdirAll(h.runtimeDir, 0755); err != nil {
		return err
	}
	for _, b := range h.builtins {
		if err := h.launch(b.name, exec.Command(b.path, b.args...)); err != nil {
			log.Printf("[pluginhost] built-in %s failed to start: %v", b.name, err)
		}
	}
	if h.pluginsDir == "" {
		return nil
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
		if err := h.launch(entry.Name(), exec.Command(bin)); err != nil {
			log.Printf("[pluginhost] %s failed to start: %v", entry.Name(), err)
		}
	}
	return nil
}

func (h *Host) launch(name string, cmd *exec.Cmd) error {
	socket := filepath.Join(h.runtimeDir, name+".sock")
	_ = os.Remove(socket)

	cmd.Env = append(os.Environ(),
		pluginapi.EnvSocket+"="+socket,
		pluginapi.EnvHandshake+"="+h.handshake,
		pluginapi.EnvAgentURL+"="+h.agentURL,
		pluginapi.EnvToken+"="+h.token,
		pluginapi.EnvDataDir+"="+h.dataDir,
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

// ExecTool invokes a tool on a running plugin over its socket and returns the textual result.
func (h *Host) ExecTool(plugin, tool string, args map[string]any) (string, error) {
	h.mu.RLock()
	m, ok := h.running[plugin]
	h.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("plugin %q not running", plugin)
	}

	body, _ := json.Marshal(map[string]any{"name": tool, "args": args})
	client := &http.Client{Transport: unixTransport(m.socket), Timeout: 30 * time.Second}
	resp, err := client.Post("http://plugin"+pluginapi.ToolExecPath, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var out struct {
		Result string `json:"result"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Error != "" {
		return "", fmt.Errorf("%s", out.Error)
	}
	return out.Result, nil
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
