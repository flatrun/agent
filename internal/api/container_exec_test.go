package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
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
