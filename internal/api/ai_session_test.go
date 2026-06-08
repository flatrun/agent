package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flatrun/agent/internal/ai"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

func newAIToolContext() *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	return c
}

// scriptedProvider returns a queued response per Complete call so a
// test can drive a multi-step tool loop.
type scriptedProvider struct {
	responses []*ai.Response
	calls     int
	lastReq   ai.Request
}

func (p *scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.lastReq = req
	if p.calls >= len(p.responses) {
		return &ai.Response{Content: "done", Model: "scripted"}, nil
	}
	resp := p.responses[p.calls]
	p.calls++
	return resp, nil
}

func (p *scriptedProvider) lastRequestMessages() []ai.Message { return p.lastReq.Messages }

func TestAISessionAutoRunToolLoop(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", &models.ServiceMetadata{Name: "myapp"})

	s.aiProvider = &scriptedProvider{responses: []*ai.Response{
		{ToolCalls: []ai.ToolCall{{ID: "c1", Name: "list_deployments", Arguments: "{}"}}, Model: "scripted"},
		{Content: "## Summary\nYou have one deployment, myapp.", Model: "scripted"},
	}}

	resp, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/ai/sessions", map[string]interface{}{
		"scope":    "system",
		"auto_run": true,
		"message":  "what deployments do I have?",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", resp.StatusCode, parsed)
	}
	if parsed["status"] != "ready" {
		t.Errorf("status = %v, want ready", parsed["status"])
	}

	messages := parsed["messages"].([]interface{})
	// user, then assistant(tool step), then assistant(final).
	if len(messages) < 2 {
		t.Fatalf("expected at least 2 turns, got %v", messages)
	}
	last := messages[len(messages)-1].(map[string]interface{})
	if last["content"] != "## Summary\nYou have one deployment, myapp." {
		t.Errorf("final content = %v", last["content"])
	}

	// The tool step must show the executed tool and its result.
	foundToolStep := false
	for _, m := range messages {
		turn := m.(map[string]interface{})
		if steps, ok := turn["tool_steps"].([]interface{}); ok && len(steps) > 0 {
			step := steps[0].(map[string]interface{})
			if step["name"] == "list_deployments" && step["result"] != nil {
				foundToolStep = true
			}
		}
	}
	if !foundToolStep {
		t.Error("auto-run did not execute the tool and record its result")
	}

	id := parsed["id"].(string)
	if _, err := s.aiSessions.Get(id); err != nil {
		t.Errorf("session not persisted: %v", err)
	}
}

func TestAISessionHidesBulkyContext(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", &models.ServiceMetadata{Name: "myapp"})

	stub := &scriptedProvider{responses: []*ai.Response{{Content: "All healthy.", Model: "scripted"}}}
	s.aiProvider = stub

	logs := "GET /health 200 OK\nGET /health 200 OK\nGET /health 200 OK"
	resp, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/ai/sessions", map[string]interface{}{
		"scope":      "deployment",
		"deployment": "myapp",
		"auto_run":   true,
		"message":    "Analyze the recent logs for myapp.",
		"context":    "```\n" + logs + "\n```",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", resp.StatusCode, parsed)
	}

	// The displayed user turn must be the short message, not the logs.
	messages := parsed["messages"].([]interface{})
	first := messages[0].(map[string]interface{})
	if first["content"] != "Analyze the recent logs for myapp." {
		t.Errorf("displayed user turn = %q, want the short message", first["content"])
	}
	if strings.Contains(first["content"].(string), "GET /health") {
		t.Error("bulky logs leaked into the displayed transcript")
	}

	// The model, however, must have received the logs.
	var prompt strings.Builder
	for _, m := range stub.lastRequestMessages() {
		prompt.WriteString(m.Content)
	}
	if !strings.Contains(prompt.String(), "GET /health 200 OK") {
		t.Error("logs were not sent to the model")
	}
}

func TestAISessionHidesSeededPrompt(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", &models.ServiceMetadata{Name: "myapp"})

	stub := &scriptedProvider{responses: []*ai.Response{{Content: "All healthy.", Model: "scripted"}}}
	s.aiProvider = stub

	resp, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/ai/sessions", map[string]interface{}{
		"scope":      "deployment",
		"deployment": "myapp",
		"auto_run":   true,
		"message":    "Analyze the recent logs for myapp.",
		"context":    "```\nGET /health 200 OK\n```",
		"seed":       true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", resp.StatusCode, parsed)
	}

	// A seeded prompt is composed by the product, not typed by the
	// operator, so the transcript starts with the assistant's answer.
	messages := parsed["messages"].([]interface{})
	for _, m := range messages {
		turn := m.(map[string]interface{})
		if turn["role"] == "user" {
			t.Errorf("seeded prompt leaked into the transcript: %v", turn["content"])
		}
	}
	if len(messages) == 0 {
		t.Fatal("expected the assistant turn in the transcript")
	}

	// The model must still receive the seeded prompt and its context.
	var prompt strings.Builder
	for _, m := range stub.lastRequestMessages() {
		prompt.WriteString(m.Content)
	}
	if !strings.Contains(prompt.String(), "Analyze the recent logs for myapp.") {
		t.Error("seeded prompt was not sent to the model")
	}
	if !strings.Contains(prompt.String(), "GET /health 200 OK") {
		t.Error("seeded context was not sent to the model")
	}
}

func TestAISessionApprovalGating(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", &models.ServiceMetadata{Name: "myapp"})

	s.aiProvider = &scriptedProvider{responses: []*ai.Response{
		{ToolCalls: []ai.ToolCall{{ID: "c1", Name: "list_networks", Arguments: "{}"}}, Model: "scripted"},
		{Content: "## Summary\nThe proxy network exists.", Model: "scripted"},
	}}

	resp, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/ai/sessions", map[string]interface{}{
		"scope":    "system",
		"auto_run": false,
		"message":  "do my networks exist?",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", resp.StatusCode, parsed)
	}
	if parsed["status"] != "awaiting_approval" {
		t.Fatalf("status = %v, want awaiting_approval", parsed["status"])
	}
	pending := parsed["pending"].([]interface{})
	if len(pending) != 1 {
		t.Fatalf("pending = %v, want 1", pending)
	}
	if pending[0].(map[string]interface{})["name"] != "list_networks" {
		t.Errorf("pending tool = %v", pending[0])
	}

	id := parsed["id"].(string)

	// A new message is rejected while approval is pending.
	resp, _ = doJSON(t, http.MethodPost, ts.URL+"/api/ai/sessions/"+id+"/messages",
		map[string]interface{}{"message": "hello"})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("message during approval = %d, want 409", resp.StatusCode)
	}

	// Approve the tool; the loop resumes and finishes.
	resp, parsed = doJSON(t, http.MethodPost, ts.URL+"/api/ai/sessions/"+id+"/approve",
		map[string]interface{}{"approved": map[string]bool{"c1": true}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve status = %d, body %v", resp.StatusCode, parsed)
	}
	if parsed["status"] != "ready" {
		t.Errorf("status after approve = %v, want ready", parsed["status"])
	}
	messages := parsed["messages"].([]interface{})
	last := messages[len(messages)-1].(map[string]interface{})
	if last["content"] != "## Summary\nThe proxy network exists." {
		t.Errorf("final content = %v", last["content"])
	}
}

func TestAISessionDeclineTool(t *testing.T) {
	s, tmpDir, ts := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", &models.ServiceMetadata{Name: "myapp"})

	s.aiProvider = &scriptedProvider{responses: []*ai.Response{
		{ToolCalls: []ai.ToolCall{{ID: "c1", Name: "list_networks", Arguments: "{}"}}, Model: "scripted"},
		{Content: "I could not inspect the networks because you declined.", Model: "scripted"},
	}}

	_, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/ai/sessions", map[string]interface{}{
		"scope": "system", "auto_run": false, "message": "check networks",
	})
	id := parsed["id"].(string)

	resp, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/ai/sessions/"+id+"/approve",
		map[string]interface{}{"approved": map[string]bool{"c1": false}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("decline status = %d", resp.StatusCode)
	}
	if parsed["status"] != "ready" {
		t.Errorf("status = %v", parsed["status"])
	}
}

func TestAISessionDisabledReturns503(t *testing.T) {
	_, _, ts := setupPlanTestServer(t)
	resp, parsed := doJSON(t, http.MethodPost, ts.URL+"/api/ai/sessions",
		map[string]interface{}{"scope": "system", "message": "hi"})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if parsed["code"] != "ai_disabled" {
		t.Errorf("code = %v", parsed["code"])
	}
}

func TestAIToolExecRefusesDestructive(t *testing.T) {
	s, tmpDir, _ := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", &models.ServiceMetadata{Name: "myapp"})

	c := newAIToolContext()
	result := s.runAITool(c, "myapp", ai.ToolCall{
		Name:      "exec_in_service",
		Arguments: `{"service":"web","command":"rm -rf /data"}`,
	})
	if !strings.Contains(result, "refused") {
		t.Errorf("destructive exec not refused: %q", result)
	}
}

func TestAIToolHostCommandRefusesDestructive(t *testing.T) {
	s, _, _ := setupPlanTestServer(t)
	c := newAIToolContext()
	result := s.runAITool(c, "", ai.ToolCall{
		Name:      "run_host_command",
		Arguments: `{"command":"rm -rf /"}`,
	})
	if !strings.Contains(result, "refused") {
		t.Errorf("destructive host command not refused: %q", result)
	}
}

func TestAIToolInstanceInfo(t *testing.T) {
	s, _, _ := setupPlanTestServer(t)
	c := newAIToolContext()
	result := s.runAITool(c, "", ai.ToolCall{Name: "get_instance_info", Arguments: "{}"})
	if !strings.Contains(result, "Hostname:") || !strings.Contains(result, "Public IP:") {
		t.Errorf("instance info missing fields: %q", result)
	}
}

func TestAIToolUnknownToolReported(t *testing.T) {
	s, _, _ := setupPlanTestServer(t)
	c := newAIToolContext()
	result := s.runAITool(c, "", ai.ToolCall{Name: "does_not_exist", Arguments: "{}"})
	if !strings.Contains(result, "unknown tool") {
		t.Errorf("unknown tool not reported: %q", result)
	}
}
