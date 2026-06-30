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

	// InfoPath is the well-known endpoint every plugin serves.
	InfoPath = "/_plugin/info"
	// HandshakeHeader carries the echoed handshake cookie on the info response.
	HandshakeHeader = "X-Flatrun-Plugin-Handshake"
)

// Info is a plugin's self-reported identity and the capabilities it requests from the host.
type Info struct {
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	DisplayName  string   `json:"display_name"`
	Description  string   `json:"description"`
	Capabilities []string `json:"capabilities,omitempty"`
}
