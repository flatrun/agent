package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/ai"
	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/files"
	"github.com/flatrun/agent/internal/networks"
	"github.com/flatrun/agent/internal/plan"
	"github.com/flatrun/agent/internal/proxy"
	"github.com/flatrun/agent/pkg/config"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func setupMCPTestServer(t *testing.T) (*Server, string, *httptest.Server) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	cfg := &config.Config{
		DeploymentsPath: tmpDir,
		Auth:            config.AuthConfig{Enabled: false},
		Infrastructure:  config.InfrastructureConfig{DefaultProxyNetwork: "proxy"},
		Nginx:           config.NginxConfig{ConfigPath: filepath.Join(tmpDir, "nginx", "conf.d")},
		Cleanup:         config.CleanupConfig{Timeout: 2 * time.Minute},
		Plans:           config.PlansConfig{TTL: time.Hour, RetentionDays: 30},
		MCP:             config.MCPConfig{Enabled: true},
	}

	s := &Server{
		config:            cfg,
		router:            gin.New(),
		manager:           docker.NewManager(tmpDir),
		networksManager:   networks.NewManager(),
		authMiddleware:    auth.NewMiddleware(&cfg.Auth),
		proxyOrchestrator: proxy.NewOrchestrator(cfg),
		planStore:         plan.NewStore(tmpDir),
		aiSessions:        ai.NewSessionStore(tmpDir),
		filesManager:      files.NewManager(tmpDir),
	}
	s.mcpHandler = s.newMCPHandler()
	s.setupRoutes()

	ts := httptest.NewServer(s.router)
	t.Cleanup(ts.Close)
	return s, tmpDir, ts
}

// TestMCPServerExposesToolsOverHTTP drives the MCP server through the real HTTP
// boundary with a real MCP client, the way an external client reaches it.
func TestMCPServerExposesToolsOverHTTP(t *testing.T) {
	_, _, ts := setupMCPTestServer(t)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/api/mcp"}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var found bool
	for _, tool := range tools.Tools {
		if tool.Name == "get_instance_info" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a built-in assistant tool to be advertised over MCP")
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_instance_info"})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if text := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "Hostname:") {
		t.Errorf("unexpected tool output %q", text)
	}
}

// TestMCPServerRefusesWhenDisabled proves the route stays mounted but rejects
// calls once mcp.enabled flips off, so the toggle takes effect without a restart.
func TestMCPServerRefusesWhenDisabled(t *testing.T) {
	s, _, ts := setupMCPTestServer(t)
	s.config.MCP.Enabled = false

	resp, err := http.Post(ts.URL+"/api/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("disabled MCP endpoint status = %d, want 503", resp.StatusCode)
	}
}

// TestMCPRegistryEnforcesActorPermissions proves the caller's actor flows
// through the bridge into the same per-tool gating the assistant uses: a viewer
// cannot drive a mutating tool.
func TestMCPRegistryEnforcesActorPermissions(t *testing.T) {
	s, tmpDir, _ := setupMCPTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", &models.ServiceMetadata{Name: "myapp"})

	viewer := &auth.ActorContext{Role: auth.RoleViewer, User: &auth.User{ID: 1, Username: "v"}}
	tool, ok := s.mcpToolRegistry(viewer).Get("control_deployment")
	if !ok {
		t.Fatal("control_deployment should be present")
	}
	out, err := tool.Handler(context.Background(), []byte(`{"deployment":"myapp","action":"restart"}`))
	if err != nil {
		t.Fatalf("handler returned a transport error: %v", err)
	}
	if !strings.Contains(out, "write access") {
		t.Errorf("a viewer must be denied a mutating tool, got %q", out)
	}
}
