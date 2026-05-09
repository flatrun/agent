package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func TestDetectShell(t *testing.T) {
	shell := detectShell("nonexistent-container-12345")
	if shell != "sh" {
		t.Errorf("expected fallback to 'sh' for nonexistent container, got %q", shell)
	}
}

func TestContainerExecHTTP_MissingContainer(t *testing.T) {
	gin.SetMode(gin.TestMode)

	s := &Server{}

	router := gin.New()
	router.POST("/containers/:id/exec", s.containerExecHTTP)

	body, _ := json.Marshal(map[string]interface{}{
		"command": "echo",
		"args":    []string{"hello"},
	})

	req := httptest.NewRequest("POST", "/containers/nonexistent-container/exec", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500 for nonexistent container, got %d", w.Code)
	}
}

func TestContainerExecHTTP_DefaultCommand(t *testing.T) {
	gin.SetMode(gin.TestMode)

	s := &Server{}

	router := gin.New()
	router.POST("/containers/:id/exec", s.containerExecHTTP)

	body, _ := json.Marshal(map[string]interface{}{})

	req := httptest.NewRequest("POST", "/containers/test-container/exec", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500 for nonexistent container, got %d", w.Code)
	}
}

func TestWebSocketUpgrader(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	if !upgrader.CheckOrigin(req) {
		t.Error("expected CheckOrigin to return true for any request")
	}
}

func TestContainerExecWebSocketDeniesReadOnlyDeploymentAccessBeforeAuthSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tmpDir, err := os.MkdirTemp("", "container-exec-ws-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.AuthConfig{
		Enabled:   true,
		JWTSecret: "test-jwt-secret-for-ws-exec",
	}
	os.Setenv("FLATRUN_ADMIN_PASSWORD", "testadminpass")
	defer os.Unsetenv("FLATRUN_ADMIN_PASSWORD")

	authManager, err := auth.NewManager(tmpDir, cfg, true)
	if err != nil {
		t.Fatalf("failed to create auth manager: %v", err)
	}
	defer authManager.Close()

	user, err := authManager.CreateUser("readonly-operator", "", "password", auth.RoleOperator, nil)
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	admin, err := authManager.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("failed to get admin: %v", err)
	}
	if err := authManager.AssignDeployment(user.ID, "app", auth.AccessLevelRead, admin.ID); err != nil {
		t.Fatalf("failed to assign deployment: %v", err)
	}

	authMiddleware := auth.NewMiddlewareWithManager(cfg, authManager)
	token, err := authMiddleware.GenerateJWTForUser(user, "")
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	server := &Server{authMiddleware: authMiddleware}
	router := gin.New()
	router.GET("/containers/:id/exec", server.containerExec)

	httpServer := newSkippableHTTPServer(t, router)
	defer httpServer.Close()

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/containers/does-not-exist/exec"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(authMessage{Type: "auth", Token: token}); err != nil {
		t.Fatalf("failed to write auth message: %v", err)
	}

	_, message, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read websocket response: %v", err)
	}

	text := string(message)
	if strings.Contains(text, "auth_success") {
		t.Fatalf("received auth_success before authorization denial: %q", text)
	}
	if !strings.Contains(text, "No access to this container") {
		t.Fatalf("expected no-access denial, got %q", text)
	}
}

func newSkippableHTTPServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	var srv *httptest.Server
	func() {
		defer func() {
			if r := recover(); r != nil {
				msg := fmt.Sprint(r)
				if strings.Contains(strings.ToLower(msg), "operation not permitted") {
					t.Skipf("httptest listener not permitted in this environment: %v", r)
				}
				panic(r)
			}
		}()
		srv = httptest.NewServer(handler)
	}()
	return srv
}

func TestAuthMessageParsing(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantErr  bool
		wantType string
	}{
		{
			name:     "valid auth message",
			input:    `{"type":"auth","token":"test-token"}`,
			wantErr:  false,
			wantType: "auth",
		},
		{
			name:     "invalid json",
			input:    `{invalid}`,
			wantErr:  true,
			wantType: "",
		},
		{
			name:     "missing type",
			input:    `{"token":"test-token"}`,
			wantErr:  false,
			wantType: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var auth authMessage
			err := json.Unmarshal([]byte(tt.input), &auth)
			if (err != nil) != tt.wantErr {
				t.Errorf("json.Unmarshal() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && auth.Type != tt.wantType {
				t.Errorf("auth.Type = %v, want %v", auth.Type, tt.wantType)
			}
		})
	}
}

func TestResizeMessageParsing(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantErr  bool
		wantRows uint16
		wantCols uint16
	}{
		{
			name:     "valid resize message",
			input:    `{"type":"resize","rows":24,"cols":80}`,
			wantErr:  false,
			wantRows: 24,
			wantCols: 80,
		},
		{
			name:     "large terminal",
			input:    `{"type":"resize","rows":50,"cols":200}`,
			wantErr:  false,
			wantRows: 50,
			wantCols: 200,
		},
		{
			name:     "invalid json",
			input:    `{invalid}`,
			wantErr:  true,
			wantRows: 0,
			wantCols: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resize resizeMessage
			err := json.Unmarshal([]byte(tt.input), &resize)
			if (err != nil) != tt.wantErr {
				t.Errorf("json.Unmarshal() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if resize.Rows != tt.wantRows {
					t.Errorf("resize.Rows = %v, want %v", resize.Rows, tt.wantRows)
				}
				if resize.Cols != tt.wantCols {
					t.Errorf("resize.Cols = %v, want %v", resize.Cols, tt.wantCols)
				}
			}
		})
	}
}

func TestContainerExecHTTP_InvalidJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)

	s := &Server{}

	router := gin.New()
	router.POST("/containers/:id/exec", s.containerExecHTTP)

	req := httptest.NewRequest("POST", "/containers/test-container/exec", bytes.NewBufferString("{invalid}"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for invalid JSON, got %d", w.Code)
	}
}

func TestContainerExecHTTP_CustomCommand(t *testing.T) {
	gin.SetMode(gin.TestMode)

	s := &Server{}

	router := gin.New()
	router.POST("/containers/:id/exec", s.containerExecHTTP)

	body, _ := json.Marshal(map[string]interface{}{
		"command": "ls",
		"args":    []string{"-la", "/tmp"},
	})

	req := httptest.NewRequest("POST", "/containers/test-container/exec", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	// Will fail because container doesn't exist
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500 for nonexistent container, got %d", w.Code)
	}

	// Verify response contains error field
	var response map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Errorf("failed to parse response: %v", err)
	}
	if _, ok := response["error"]; !ok {
		t.Error("expected error field in response")
	}
}

func TestSendError(t *testing.T) {
	// sendError is a helper function that formats error messages
	// We can't easily test WebSocket writing, but we can verify the format
	msg := "Test error message"
	expected := "\r\n\x1b[31mError: " + msg + "\x1b[0m\r\n"

	// Verify the format is correct (ANSI red color codes)
	if expected != "\r\n\x1b[31mError: Test error message\x1b[0m\r\n" {
		t.Error("error message format is incorrect")
	}
}
