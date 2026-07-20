package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/flatrun/agent/internal/ai"
	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/contextkeys"
	"github.com/flatrun/agent/pkg/version"
	"github.com/gin-gonic/gin"
	"github.com/whilesmartgo/agents"
	agentsmcp "github.com/whilesmartgo/mcp"
)

// mcpActorKey carries the request's authenticated actor into the MCP handler's
// per-request registry callback, which only sees the raw *http.Request.
type mcpActorKey struct{}

// newMCPHandler builds the HTTP handler that serves the assistant tool set over
// MCP. The registry is resolved per request, so every call is authorized as the
// actor on that request rather than whoever opened the session.
func (s *Server) newMCPHandler() http.Handler {
	return agentsmcp.StreamableHTTPHandler("flatrun", version.Get().Version, func(r *http.Request) *agents.Registry {
		actor, _ := r.Context().Value(mcpActorKey{}).(*auth.ActorContext)
		return s.mcpToolRegistry(actor)
	})
}

// mcpHTTP adapts the gin request to the MCP handler, passing the actor the auth
// middleware already resolved so tool handlers can gate on it.
func (s *Server) mcpHTTP(c *gin.Context) {
	ctx := context.WithValue(c.Request.Context(), mcpActorKey{}, auth.GetActorFromContext(c))
	s.mcpHandler.ServeHTTP(c.Writer, c.Request.WithContext(ctx))
}

// mcpToolRegistry wraps every assistant tool (built-in and plugin-contributed)
// as an agents.Tool bound to actor. Each handler runs through runAITool, the
// same dispatch the assistant uses, so authorization, protected-mode gating,
// plugin routing, and output truncation are identical.
func (s *Server) mcpToolRegistry(actor *auth.ActorContext) *agents.Registry {
	specs := s.aiToolSpecs()
	tools := make([]agents.Tool, 0, len(specs))
	for _, spec := range specs {
		spec := spec
		tools = append(tools, agents.Tool{
			Name:        spec.Name,
			Description: spec.Description,
			Parameters:  spec.Parameters,
			Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
				gc := toolGinContext(ctx, actor)
				// runAITool returns tool errors as content (prefixed "Error: "),
				// matching what the assistant sees, so an MCP client's model can
				// read the failure and adapt rather than the call faulting.
				return s.runAITool(gc, "", ai.ToolCall{Name: spec.Name, Arguments: string(raw)}), nil
			},
		})
	}
	return agents.NewRegistry(tools...)
}

// toolGinContext builds the minimal gin.Context the tool dispatch expects: a
// request carrying ctx (for per-tool timeouts) and the actor for permission
// checks. A nil actor means auth is disabled, as elsewhere in the API.
func toolGinContext(ctx context.Context, actor *auth.ActorContext) *gin.Context {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
	gc := &gin.Context{Request: req}
	if actor != nil {
		gc.Set(contextkeys.Actor, actor)
	}
	return gc
}
