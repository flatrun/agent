// Command hello is a sample FlatRun plugin: a standalone binary that serves an API the agent
// reverse-proxies under /api/v1/plugins/hello/. Build it with:
//
//	go build -o plugin ./examples/plugins/hello
//
// and drop the resulting "plugin" binary in <deployments>/.flatrun/plugins/hello/.
package main

import (
	"net/http"

	"github.com/flatrun/agent/pkg/pluginapi"
	"github.com/flatrun/agent/pkg/pluginsdk"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"hello from the flatrun plugin"}`))
	})

	_ = pluginsdk.Serve(pluginapi.Info{
		Name:        "hello",
		Version:     "0.1.0",
		DisplayName: "Hello Plugin",
		Description: "A sample out-of-process plugin.",
	}, mux)
}
