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
		config:          &config.Config{Security: config.SecurityConfig{AutoBlockDuration: time.Hour}},
		securityManager: manager,
	}
	router := gin.New()
	router.POST("/api/security/events/ingest", server.ingestSecurityEvent)

	body := []byte(`{"incident_id":"FR-1234ABCDEF56","source_ip":"203.0.113.30","request_path":"/api/orders","request_method":"GET","status_code":502,"user_agent":"curl/8.0","deployment_name":"shop.example.com","timestamp":1787306400}`)
	request := httptest.NewRequest(http.MethodPost, "/api/security/events/ingest", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
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
