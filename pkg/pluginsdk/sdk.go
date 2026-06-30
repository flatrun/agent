// Package pluginsdk is imported by FlatRun plugin binaries to serve their routes over the
// host-provided socket. See examples/plugins/hello for usage.
package pluginsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/flatrun/agent/pkg/pluginapi"
)

// Serve runs the plugin until the host stops it. handler serves the plugin's own routes;
// pass nil if the plugin only reports info. It returns an error if the process was not
// launched by the host (the socket env var is unset) or the socket cannot be served.
func Serve(info pluginapi.Info, handler http.Handler) error {
	socket := os.Getenv(pluginapi.EnvSocket)
	if socket == "" {
		return fmt.Errorf("not launched by the flatrun plugin host: %s is unset", pluginapi.EnvSocket)
	}
	handshake := os.Getenv(pluginapi.EnvHandshake)

	mux := http.NewServeMux()
	mux.HandleFunc(pluginapi.InfoPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(pluginapi.HandshakeHeader, handshake)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(info)
	})
	if handler != nil {
		mux.Handle("/", handler)
	}

	// A stale socket file from a previous run would block Listen.
	_ = os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("listen on plugin socket: %w", err)
	}

	srv := &http.Server{Handler: mux}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// AgentCallback returns the agent API base URL and token a plugin can use to call back into
// the agent. Either may be empty when the host did not grant callback access.
func AgentCallback() (baseURL, token string) {
	return os.Getenv(pluginapi.EnvAgentURL), os.Getenv(pluginapi.EnvToken)
}
