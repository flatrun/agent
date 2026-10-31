package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/security"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
)

func TestSecurityIngestPreservesIncidentID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager, err := security.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer manager.Close()

	server := &Server{
		config:          &config.Config{Security: config.SecurityConfig{AutoBlockDuration: time.Hour, InternalAPIToken: "proxy-test-token"}},
		securityManager: manager,
	}
	router := gin.New()
	router.POST("/api/security/events/ingest", server.ingestSecurityEvent)

	body := []byte(`{"incident_id":"FR-1234ABCDEF56","source_ip":"203.0.113.30","request_path":"/api/orders","request_method":"GET","status_code":502,"user_agent":"curl/8.0","deployment_name":"shop.example.com","timestamp":1787306400}`)
	request := httptest.NewRequest(http.MethodPost, "/api/security/events/ingest", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Internal-Token", "proxy-test-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Event security.SecurityEvent `json:"event"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Event.IncidentID != "FR-1234ABCDEF56" {
		t.Fatalf("incident_id = %q", payload.Event.IncidentID)
	}
}

func TestSecurityIPMutationsRejectShellPayloads(t *testing.T) {
	manager, err := security.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	server := &Server{config: &config.Config{}, securityManager: manager}
	router := gin.New()
	router.POST("/block", server.blockIP)
	router.DELETE("/block/:ip", server.unblockIP)
	for _, entry := range []struct{ method, path, body string }{
		{"POST", "/block", `{"ip":"x';id;'"}`},
		{"DELETE", "/block/x%27%3Bid%3B%27", ""},
	} {
		req := httptest.NewRequest(entry.method, entry.path, bytes.NewBufferString(entry.body))
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		if out.Code != http.StatusBadRequest {
			t.Fatalf("invalid IP accepted: %d %s", out.Code, out.Body.String())
		}
	}
}
