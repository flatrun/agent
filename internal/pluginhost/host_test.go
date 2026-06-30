package pluginhost

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The unix reverse proxy forwards a request to a plugin listening on its socket.
func TestUnixProxyForwards(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "p.sock")

	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hello" {
			_, _ = w.Write([]byte("hi from plugin"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	proxy := newUnixProxy(socket)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello", nil))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "hi from plugin") {
		t.Fatalf("proxy did not forward to the plugin: code=%d body=%q", rec.Code, rec.Body.String())
	}
}

// End to end: build the sample plugin binary, launch it through the host, and proxy a request.
func TestHostLaunchesAndProxiesBinaryPlugin(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess build in short mode")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not available")
	}

	base := t.TempDir()
	helloDir := filepath.Join(base, "plugins", "hello")
	if err := os.MkdirAll(helloDir, 0755); err != nil {
		t.Fatal(err)
	}

	build := exec.Command(goBin, "build", "-o", filepath.Join(helloDir, "plugin"), "github.com/flatrun/agent/examples/plugins/hello")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building sample plugin: %v\n%s", err, out)
	}

	h := New(filepath.Join(base, "plugins"), filepath.Join(base, "run"), "", "")
	if err := h.Start(); err != nil {
		t.Fatal(err)
	}
	defer h.Stop()

	if len(h.Infos()) != 1 || h.Infos()[0].Name != "hello" {
		t.Fatalf("expected the hello plugin to be running, got %+v", h.Infos())
	}

	proxy, ok := h.Proxy("hello")
	if !ok {
		t.Fatal("hello plugin proxy not found")
	}
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "hello from the flatrun plugin") {
		t.Fatalf("proxied plugin response = code %d body %q", rec.Code, rec.Body.String())
	}

	// Stop terminates the subprocess and clears the socket.
	h.Stop()
	time.Sleep(50 * time.Millisecond)
	if _, ok := h.Proxy("hello"); ok {
		t.Error("plugin should be gone after Stop")
	}
}
