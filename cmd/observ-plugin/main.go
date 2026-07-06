// Command observ-plugin is the FlatRun observability app as a standalone binary. The agent
// normally runs this logic by re-execing itself; this command allows building the plugin on
// its own for development or as an external plugin.
//
// Build: go build -o plugin ./cmd/observ-plugin
package main

import "github.com/flatrun/agent/internal/observ"

func main() {
	_ = observ.RunPlugin()
}
