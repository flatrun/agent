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

// Tool is a plugin capability the AI assistant can invoke. Run receives the parsed args and
// returns the textual result the model reads.
type Tool struct {
	Spec pluginapi.ToolSpec
	Run  func(args map[string]any) (string, error)
}

// Serve runs the plugin until the host stops it. handler serves the plugin's own routes (nil
// if the plugin only reports info); tools are advertised to the assistant and dispatched over
// the tool-exec endpoint. It returns an error if the process was not launched by the host
// (the socket env var is unset) or the socket cannot be served.
func Serve(info pluginapi.Info, handler http.Handler, tools ...Tool) error {
	socket := os.Getenv(pluginapi.EnvSocket)
	if socket == "" {
		return fmt.Errorf("not launched by the flatrun plugin host: %s is unset", pluginapi.EnvSocket)
	}
	handshake := os.Getenv(pluginapi.EnvHandshake)

	byName := make(map[string]Tool, len(tools))
	for _, t := range tools {
		byName[t.Spec.Name] = t
		info.Tools = append(info.Tools, t.Spec)
	}

	mux := http.NewServeMux()
	mux.HandleFunc(pluginapi.InfoPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(pluginapi.HandshakeHeader, handshake)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(info)
	})
	mux.HandleFunc(pluginapi.ToolExecPath, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string         `json:"name"`
			Args map[string]any `json:"args"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		tool, ok := byName[req.Name]
		if !ok {
			http.Error(w, "unknown tool", http.StatusNotFound)
			return
		}
		result, err := tool.Run(req.Args)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"result": result})
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
