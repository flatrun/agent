package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/contextkeys"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

func toolCtx(actor *auth.ActorContext) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	if actor != nil {
		c.Set(contextkeys.Actor, actor)
	}
	return c
}

func TestWriteDeploymentFileTool(t *testing.T) {
	s, tmpDir, _ := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", &models.ServiceMetadata{Name: "myapp"})

	tool := s.aiToolRegistry()["write_deployment_file"]
	out, err := tool.Run(s, toolCtx(nil), "myapp", map[string]interface{}{
		"path":    "config/app.conf",
		"content": "key = value\n",
	})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if !strings.Contains(out, "app.conf") {
		t.Errorf("unexpected output %q", out)
	}
	data, err := os.ReadFile(filepath.Join(tmpDir, "myapp", "config", "app.conf"))
	if err != nil || string(data) != "key = value\n" {
		t.Errorf("file not written correctly: %q err=%v", string(data), err)
	}
}

func TestWriteDeploymentFileRefusesTraversal(t *testing.T) {
	s, tmpDir, _ := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", &models.ServiceMetadata{Name: "myapp"})

	tool := s.aiToolRegistry()["write_deployment_file"]
	if _, err := tool.Run(s, toolCtx(nil), "myapp", map[string]interface{}{
		"path":    "../escape.txt",
		"content": "x",
	}); err == nil {
		t.Fatal("a path escaping the deployment directory must be refused")
	}
}

func TestWriteDeploymentFileFailsClosedOnProtectedCheckError(t *testing.T) {
	s, _, _ := setupPlanTestServer(t)
	// No deployment is created, so the protected-mode check cannot load it and
	// errors. A state-changing tool must refuse rather than proceed.
	tool := s.aiToolRegistry()["write_deployment_file"]
	_, err := tool.Run(s, toolCtx(nil), "ghost", map[string]interface{}{"path": "x", "content": "y"})
	if err == nil || !strings.Contains(err.Error(), "protected mode") {
		t.Errorf("a failed protected-mode check must block the write, got %v", err)
	}
}

func TestMutatingToolsRequireWriteAccess(t *testing.T) {
	s, tmpDir, _ := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", &models.ServiceMetadata{Name: "myapp"})

	// A viewer with no write access to the deployment.
	viewer := &auth.ActorContext{Role: auth.RoleViewer, User: &auth.User{ID: 1, Username: "v"}}
	args := map[string]interface{}{"path": "x", "content": "y", "action_id": "a", "action": "restart"}
	for _, name := range []string{"write_deployment_file", "run_quick_action", "control_deployment"} {
		tool := s.aiToolRegistry()[name]
		if _, err := tool.Run(s, toolCtx(viewer), "myapp", args); err == nil || !strings.Contains(err.Error(), "write access") {
			t.Errorf("%s must require write access, got %v", name, err)
		}
	}
}

func TestGetSecurityEventsWhenDisabled(t *testing.T) {
	s, tmpDir, _ := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", &models.ServiceMetadata{Name: "myapp"})

	tool := s.aiToolRegistry()["get_security_events"]
	out, err := tool.Run(s, toolCtx(nil), "myapp", map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "not enabled") {
		t.Errorf("expected a disabled message, got %q", out)
	}
}

func TestControlDeploymentRejectsBadAction(t *testing.T) {
	s, tmpDir, _ := setupPlanTestServer(t)
	createTestDeployment(t, tmpDir, "myapp", &models.ServiceMetadata{Name: "myapp"})

	tool := s.aiToolRegistry()["control_deployment"]
	if _, err := tool.Run(s, toolCtx(nil), "myapp", map[string]interface{}{"action": "delete"}); err == nil ||
		!strings.Contains(err.Error(), "start, stop, or restart") {
		t.Errorf("expected an invalid-action error, got %v", err)
	}
}
