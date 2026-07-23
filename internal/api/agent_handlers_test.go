package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flatrun/agent/internal/ai"
	"github.com/flatrun/agent/pkg/models"
)

func writeAgentFile(t *testing.T, tmpDir, name, content string) {
	t.Helper()
	dir := filepath.Join(tmpDir, ".flatrun", "agents")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestListAgents(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	s.aiAgents = ai.NewAgentStore(tmpDir)
	writeAgentFile(t, tmpDir, "disk-report.md", "---\ndescription: Report disk usage\n---\nSummarize disk usage.")

	resp, parsed := doJSON(t, http.MethodGet, ts.URL+"/api/ai/agents", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", resp.StatusCode, parsed)
	}
	agents := parsed["agents"].([]interface{})
	if len(agents) != 1 {
		t.Fatalf("agents = %v", agents)
	}
	first := agents[0].(map[string]interface{})
	if first["name"] != "disk-report" || first["description"] != "Report disk usage" {
		t.Errorf("unexpected agent: %v", first)
	}
}

func TestRunAgentExecutesAsSession(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	s.aiAgents = ai.NewAgentStore(tmpDir)
	writeAgentFile(t, tmpDir, "hello.md", "Say hello and stop.")

	s.aiProvider = &scriptedProvider{responses: []*ai.Response{{Content: "Hello.", Model: "scripted"}}}

	resp, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/ai/agents/hello/run", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", resp.StatusCode, parsed)
	}
	if parsed["status"] != "ready" {
		t.Errorf("status = %v", parsed["status"])
	}
	// The transcript shows the short label, not the instruction body.
	messages := parsed["messages"].([]interface{})
	first := messages[0].(map[string]interface{})
	if first["content"] != `Run the "hello" agent` {
		t.Errorf("displayed turn = %v", first["content"])
	}
	// The run records which agent it belongs to, so an agent's run history
	// is just its sessions.
	if parsed["agent"] != "hello" {
		t.Errorf("agent = %v, want hello", parsed["agent"])
	}
	// The run is a persisted session, resumable like any other.
	saved, err := s.aiSessions.Get(parsed["id"].(string))
	if err != nil {
		t.Fatalf("run not persisted as a session: %v", err)
	}
	if saved.Agent != "hello" {
		t.Errorf("persisted agent = %q, want hello", saved.Agent)
	}
}

func TestRunAgentPausesForMutatingTools(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	s.aiAgents = ai.NewAgentStore(tmpDir)
	createTestDeployment(t, tmpDir, "myapp", &models.ServiceMetadata{Name: "myapp"})
	writeAgentFile(t, tmpDir, "fix-config.md",
		"---\nscope: deployment\ndeployment: myapp\n---\nEnsure conf/app.conf sets key to value.")

	s.aiProvider = &scriptedProvider{responses: []*ai.Response{
		{ToolCalls: []ai.ToolCall{{ID: "c1", Name: "write_deployment_file",
			Arguments: `{"path":"conf/app.conf","content":"key = value\n"}`}}, Model: "scripted"},
		{Content: "Done.", Model: "scripted"},
	}}

	resp, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/ai/agents/fix-config/run", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", resp.StatusCode, parsed)
	}
	// A agent run must not bypass the approval gate on state changes.
	if parsed["status"] != "awaiting_approval" {
		t.Fatalf("status = %v, want awaiting_approval", parsed["status"])
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "myapp", "conf", "app.conf")); !os.IsNotExist(err) {
		t.Fatal("the file must not be written before approval")
	}
}

func TestRunAgentRedactsSecretsFromInstructions(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	s.aiAgents = ai.NewAgentStore(tmpDir)
	createTestDeployment(t, tmpDir, "myapp", &models.ServiceMetadata{Name: "myapp"})
	envContent := "DB_PASSWORD=hunter2secret\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "myapp", ".env.flatrun"), []byte(envContent), 0600); err != nil {
		t.Fatal(err)
	}
	writeAgentFile(t, tmpDir, "leaky.md",
		"---\nscope: deployment\ndeployment: myapp\n---\nThe password is hunter2secret; check the database.")

	stub := &scriptedProvider{responses: []*ai.Response{{Content: "Checked.", Model: "scripted"}}}
	s.aiProvider = stub

	if resp, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/ai/agents/leaky/run", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", resp.StatusCode, parsed)
	}
	var sent strings.Builder
	for _, m := range stub.lastRequestMessages() {
		sent.WriteString(m.Content)
	}
	if strings.Contains(sent.String(), "hunter2secret") {
		t.Error("a secret in the agent file reached the provider")
	}
}

func TestAgentEditorRoundTrip(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	s.aiAgents = ai.NewAgentStore(tmpDir)

	content := "---\ndescription: Report disk usage\n---\nSummarize disk usage."
	resp, parsed := doJSON(t, http.MethodPut, ts.URL+"/api/ai/agents/disk-report",
		map[string]interface{}{"content": content})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put status = %d, body %v", resp.StatusCode, parsed)
	}

	resp, parsed = doJSON(t, http.MethodGet, ts.URL+"/api/ai/agents/disk-report", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, body %v", resp.StatusCode, parsed)
	}
	if parsed["content"] != content {
		t.Errorf("content = %v", parsed["content"])
	}

	// An invalid definition is rejected, not written.
	resp, _ = doJSON(t, http.MethodPut, ts.URL+"/api/ai/agents/broken",
		map[string]interface{}{"content": "---\nscope: galaxy\n---\nX."})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid definition status = %d, want 400", resp.StatusCode)
	}

	resp, _ = doJSON(t, http.MethodDelete, ts.URL+"/api/ai/agents/disk-report", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("delete status = %d", resp.StatusCode)
	}
	if _, err := s.aiAgents.Get("disk-report"); err != ai.ErrAgentNotFound {
		t.Errorf("agent still present after delete: %v", err)
	}
}

func TestWriteAgentFileToolCreatesRunnableAgent(t *testing.T) {
	s, tmpDir, _ := setupPlanTestServer(t)
	s.aiAgents = ai.NewAgentStore(tmpDir)

	tool := s.aiToolRegistry()["write_agent_file"]
	out, err := tool.Run(s, toolCtx(nil), "", map[string]interface{}{
		"name":    "hello",
		"content": "Say hello to the operator.",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("unexpected output %q", out)
	}
	if _, err := s.aiAgents.Get("hello"); err != nil {
		t.Errorf("agent not runnable after tool write: %v", err)
	}

	// An invalid definition must be refused, not written.
	if _, err := tool.Run(s, toolCtx(nil), "", map[string]interface{}{
		"name": "bad", "content": "---\nscope: deployment\n---\nX.",
	}); err == nil {
		t.Error("an invalid definition should be rejected")
	}
}

func TestAutoApprovePolicyRunsWriteWithoutPause(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	s.aiAgents = ai.NewAgentStore(tmpDir)
	createTestDeployment(t, tmpDir, "myapp", &models.ServiceMetadata{Name: "myapp"})
	writeAgentFile(t, tmpDir, "fixer.md",
		"---\nscope: deployment\ndeployment: myapp\npolicy:\n  auto_approve: [write_deployment_file]\n---\nEnsure conf/app.conf sets key to value.")

	s.aiProvider = &scriptedProvider{responses: []*ai.Response{
		{ToolCalls: []ai.ToolCall{{ID: "c1", Name: "write_deployment_file",
			Arguments: `{"path":"conf/app.conf","content":"key = value\n"}`}}, Model: "scripted"},
		{Content: "Done.", Model: "scripted"},
	}}

	resp, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/ai/agents/fixer/run", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", resp.StatusCode, parsed)
	}
	if parsed["status"] != "ready" {
		t.Fatalf("status = %v, want ready: an auto-approved write must not pause", parsed["status"])
	}
	data, err := os.ReadFile(filepath.Join(tmpDir, "myapp", "conf", "app.conf"))
	if err != nil || string(data) != "key = value\n" {
		t.Errorf("auto-approved write not applied: %q err=%v", string(data), err)
	}
}

func TestRequireApprovalPolicyPausesReadTool(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	s.aiAgents = ai.NewAgentStore(tmpDir)
	writeAgentFile(t, tmpDir, "careful.md",
		"---\npolicy:\n  require_approval: [list_networks]\n---\nList the networks.")

	s.aiProvider = &scriptedProvider{responses: []*ai.Response{
		{ToolCalls: []ai.ToolCall{{ID: "c1", Name: "list_networks", Arguments: "{}"}}, Model: "scripted"},
		{Content: "Done.", Model: "scripted"},
	}}

	resp, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/ai/agents/careful/run", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", resp.StatusCode, parsed)
	}
	if parsed["status"] != "awaiting_approval" {
		t.Errorf("status = %v: require_approval must pause even a read-only tool", parsed["status"])
	}
}

func TestDenyPolicyHidesTool(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	s.aiAgents = ai.NewAgentStore(tmpDir)
	createTestDeployment(t, tmpDir, "myapp", &models.ServiceMetadata{Name: "myapp"})
	writeAgentFile(t, tmpDir, "restrained.md",
		"---\nscope: deployment\ndeployment: myapp\npolicy:\n  deny: [control_deployment]\n---\nRestart the deployment.")

	stub := &scriptedProvider{responses: []*ai.Response{
		{ToolCalls: []ai.ToolCall{{ID: "c1", Name: "control_deployment",
			Arguments: `{"action":"restart"}`}}, Model: "scripted"},
		{Content: "Could not.", Model: "scripted"},
	}}
	s.aiProvider = stub

	resp, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/ai/agents/restrained/run", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", resp.StatusCode, parsed)
	}
	// The denied tool is not registered: the call fails as unknown rather than
	// pausing or executing, and the tool is not advertised to the model.
	if parsed["status"] != "ready" {
		t.Errorf("status = %v", parsed["status"])
	}
	for _, tool := range stub.lastReq.Tools {
		if tool.Name == "control_deployment" {
			t.Error("a denied tool must not be advertised to the model")
		}
	}
}

func TestDryRunDeclinesWritesWithoutPausing(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	s.aiAgents = ai.NewAgentStore(tmpDir)
	createTestDeployment(t, tmpDir, "myapp", &models.ServiceMetadata{Name: "myapp"})
	writeAgentFile(t, tmpDir, "fixer.md",
		"---\nscope: deployment\ndeployment: myapp\n---\nEnsure conf/app.conf sets key to value.")

	s.aiProvider = &scriptedProvider{responses: []*ai.Response{
		{ToolCalls: []ai.ToolCall{{ID: "c1", Name: "write_deployment_file",
			Arguments: `{"path":"conf/app.conf","content":"key = value\n"}`}}, Model: "scripted"},
		{Content: "Reported.", Model: "scripted"},
	}}

	resp, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/ai/agents/fixer/run",
		map[string]interface{}{"dry_run": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", resp.StatusCode, parsed)
	}
	if parsed["status"] != "ready" || parsed["dry_run"] != true {
		t.Fatalf("status = %v dry_run = %v", parsed["status"], parsed["dry_run"])
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "myapp", "conf", "app.conf")); !os.IsNotExist(err) {
		t.Fatal("a dry run must not write the file")
	}
	// The decline is reported to the model as the tool result.
	found := false
	for _, m := range parsed["messages"].([]interface{}) {
		if steps, ok := m.(map[string]interface{})["tool_steps"].([]interface{}); ok {
			for _, st := range steps {
				if r, _ := st.(map[string]interface{})["result"].(string); strings.Contains(r, "dry run") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("expected a dry-run decline in the tool results")
	}
}

func TestAgentMaxStepsHonored(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	s.aiAgents = ai.NewAgentStore(tmpDir)
	writeAgentFile(t, tmpDir, "looper.md", "---\nmax_steps: 1\n---\nKeep listing networks.")

	loop := &ai.Response{ToolCalls: []ai.ToolCall{{ID: "c", Name: "list_networks", Arguments: "{}"}}, Model: "scripted"}
	stub := &scriptedProvider{responses: []*ai.Response{loop, loop, loop}}
	s.aiProvider = stub

	resp, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/ai/agents/looper/run", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", resp.StatusCode, parsed)
	}
	if stub.calls != 1 {
		t.Errorf("engine calls = %d, want exactly max_steps", stub.calls)
	}
}

func TestRunAgentNotFound(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	s.aiAgents = ai.NewAgentStore(tmpDir)
	s.aiProvider = &scriptedProvider{}

	resp, _ := doJSON(t, http.MethodPost, ts.URL+"/api/ai/agents/ghost/run", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}
