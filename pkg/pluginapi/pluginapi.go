// Package pluginapi is the dependency-free wire contract between the agent and a plugin
// binary: the plugin serves /_plugin/info (echoing the handshake) plus its own routes on the
// host-provided unix socket.
package pluginapi

const (
	// EnvSocket is the unix socket path the plugin must listen on.
	EnvSocket = "FLATRUN_PLUGIN_SOCKET"
	// EnvHandshake is a per-launch cookie the plugin echoes so the host knows it spoke to
	// the process it actually started, not something else bound to the socket.
	EnvHandshake = "FLATRUN_PLUGIN_HANDSHAKE"
	// EnvAgentURL and EnvToken let a plugin call back into the agent API.
	EnvAgentURL = "FLATRUN_AGENT_URL"
	EnvToken    = "FLATRUN_PLUGIN_TOKEN"
	// EnvDataDir is the deployments base directory, so a plugin can read/write flat state
	// (e.g. its config under .flatrun/).
	EnvDataDir = "FLATRUN_DATA_DIR"

	// InfoPath is the well-known endpoint every plugin serves.
	InfoPath = "/_plugin/info"
	// HandshakeHeader carries the echoed handshake cookie on the info response.
	HandshakeHeader = "X-Flatrun-Plugin-Handshake"
)

// Info is a plugin's self-reported identity, the capabilities it requests from the host, and
// how it contributes UI: which native slots it fills and any configuration it accepts.
type Info struct {
	Name         string         `json:"name"`
	Version      string         `json:"version"`
	DisplayName  string         `json:"display_name"`
	Description  string         `json:"description"`
	Capabilities []string       `json:"capabilities,omitempty"`
	UIExtensions []UIExtension  `json:"ui_extensions,omitempty"`
	ConfigSchema map[string]any `json:"config_schema,omitempty"`
	Tools        []ToolSpec     `json:"tools,omitempty"`
}

// ToolSpec declares a tool the plugin exposes to the AI assistant. The host advertises it to
// the model and dispatches calls back to the plugin's /_plugin/tools/exec endpoint. Mutates
// marks a tool that changes state, so the host can gate it on write access. Global marks a
// mutating tool that acts on a host-wide setting rather than a single deployment, so the host
// gates it on a settings-write permission instead of requiring a deployment scope (which such
// a tool has no way to provide, and which would make it fail in an unscoped assistant session).
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Mutates     bool           `json:"mutates,omitempty"`
	Global      bool           `json:"global,omitempty"`
}

// ToolExecPath is where the host POSTs {name, args} to invoke a plugin tool.
const ToolExecPath = "/_plugin/tools/exec"

// UIExtension declares that a plugin contributes UI into a named slot. The host renders a
// native component for Kind (plugins never ship UI code) and feeds it from Endpoint, a path
// on the plugin's own API.
type UIExtension struct {
	Slot     string `json:"slot"` // "deployment.detail" | "settings" | "overview"
	Kind     string `json:"kind"` // "metrics-panel" | "form" | "timeline"
	Title    string `json:"title,omitempty"`
	Icon     string `json:"icon,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
}
