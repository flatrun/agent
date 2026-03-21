package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/networks"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func setupTestSetupServer(t *testing.T) (*Server, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	tmpDir, err := os.MkdirTemp("", "flatrun-setup-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	flatrunDir := filepath.Join(tmpDir, ".flatrun")
	if err := os.MkdirAll(flatrunDir, 0755); err != nil {
		t.Fatalf("Failed to create .flatrun dir: %v", err)
	}

	cfg := &config.Config{
		DeploymentsPath: tmpDir,
		API: config.APIConfig{
			Host:           "0.0.0.0",
			Port:           8090,
			EnableCORS:     true,
			AllowedOrigins: []string{"*"},
		},
		Auth: config.AuthConfig{
			Enabled: true,
		},
		Domain: config.DomainConfig{
			AutoSSL: true,
		},
		Infrastructure: config.InfrastructureConfig{
			DefaultProxyNetwork: "proxy",
		},
	}

	router := gin.New()
	router.Use(cors.New(cors.Config{
		AllowAllOrigins: true,
		AllowMethods:    []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:    []string{"Content-Type", "Authorization", "Accept"},
	}))
	authMiddleware := auth.NewMiddleware(&cfg.Auth)
	authManager, err := auth.NewManager(tmpDir, &cfg.Auth, false)
	if err != nil {
		t.Fatalf("Failed to create auth manager: %v", err)
	}
	authMiddleware.SetManager(authManager)

	configPath := filepath.Join(tmpDir, "config.yml")
	if err := config.Save(cfg, configPath); err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	s := &Server{
		config:          cfg,
		configPath:      configPath,
		router:          router,
		manager:         docker.NewManager(tmpDir),
		networksManager: networks.NewManager(),
		authMiddleware:  authMiddleware,
		authManager:     authManager,
	}

	s.setupRoutes()
	return s, tmpDir
}

func TestGetSetupStatus(t *testing.T) {
	s, tmpDir := setupTestSetupServer(t)
	defer os.RemoveAll(tmpDir)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/setup/status", nil)
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if resp["initialized"] != false {
		t.Error("Expected initialized to be false")
	}
}

func TestGetSetupInfo(t *testing.T) {
	s, tmpDir := setupTestSetupServer(t)
	defer os.RemoveAll(tmpDir)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/setup/info", nil)
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if _, ok := resp["agent_version"]; !ok {
		t.Error("Expected agent_version in response")
	}
	if _, ok := resp["instance_ip"]; !ok {
		t.Error("Expected instance_ip in response")
	}
}

func TestSetupGuardBlocksAfterComplete(t *testing.T) {
	s, tmpDir := setupTestSetupServer(t)
	defer os.RemoveAll(tmpDir)

	if err := s.markSetupComplete(); err != nil {
		t.Fatalf("Failed to mark setup complete: %v", err)
	}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/setup/validate", nil)
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("Expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSetupStatusAfterComplete(t *testing.T) {
	s, tmpDir := setupTestSetupServer(t)
	defer os.RemoveAll(tmpDir)

	if err := s.markSetupComplete(); err != nil {
		t.Fatalf("Failed to mark setup complete: %v", err)
	}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/setup/status", nil)
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["initialized"] != true {
		t.Error("Expected initialized to be true after completion")
	}
}

func TestValidateSystem(t *testing.T) {
	s, tmpDir := setupTestSetupServer(t)
	defer os.RemoveAll(tmpDir)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/setup/validate", nil)
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	checks, ok := resp["checks"].([]interface{})
	if !ok {
		t.Fatal("Expected checks array in response")
	}

	if len(checks) == 0 {
		t.Error("Expected at least one check")
	}
}

func TestVerifyDNSMissingParam(t *testing.T) {
	s, tmpDir := setupTestSetupServer(t)
	defer os.RemoveAll(tmpDir)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/setup/verify-dns", nil)
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("Expected 400, got %d", w.Code)
	}
}

func TestConfigureSettings(t *testing.T) {
	s, tmpDir := setupTestSetupServer(t)
	defer os.RemoveAll(tmpDir)

	body := `{"domain": "test.example.com", "auto_ssl": false, "cors_origins": ["https://panel.example.com"]}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/setup/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if s.config.Domain.DefaultDomain != "test.example.com" {
		t.Errorf("Expected domain to be test.example.com, got %s", s.config.Domain.DefaultDomain)
	}
	if s.config.Domain.AutoSSL != false {
		t.Error("Expected auto_ssl to be false")
	}

	found := false
	for _, o := range s.config.API.AllowedOrigins {
		if o == "https://panel.example.com" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected CORS origin to be added")
	}
}

func TestConfigureAuthenticationPassword(t *testing.T) {
	s, tmpDir := setupTestSetupServer(t)
	defer os.RemoveAll(tmpDir)

	body := `{"auth_method": "password", "username": "testadmin", "password": "securepass123", "email": "test@example.com"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/setup/authentication", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["username"] != "testadmin" {
		t.Errorf("Expected username testadmin, got %v", resp["username"])
	}
	if resp["auth_method"] != "password" {
		t.Errorf("Expected auth_method password, got %v", resp["auth_method"])
	}
}

func TestConfigureAuthenticationAPIKey(t *testing.T) {
	s, tmpDir := setupTestSetupServer(t)
	defer os.RemoveAll(tmpDir)

	body := `{"auth_method": "apikey"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/setup/authentication", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["api_key"] == nil || resp["api_key"] == "" {
		t.Error("Expected api_key in response")
	}
}

func TestConfigureAuthenticationBoth(t *testing.T) {
	s, tmpDir := setupTestSetupServer(t)
	defer os.RemoveAll(tmpDir)

	body := `{"auth_method": "both", "username": "admin", "password": "securepass123"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/setup/authentication", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["username"] == nil {
		t.Error("Expected username in response")
	}
	if resp["api_key"] == nil || resp["api_key"] == "" {
		t.Error("Expected api_key in response")
	}
}

func TestConfigureAuthenticationShortPassword(t *testing.T) {
	s, tmpDir := setupTestSetupServer(t)
	defer os.RemoveAll(tmpDir)

	body := `{"auth_method": "password", "username": "admin", "password": "short"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/setup/authentication", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("Expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCompleteSetup(t *testing.T) {
	s, tmpDir := setupTestSetupServer(t)
	defer os.RemoveAll(tmpDir)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/setup/complete", nil)
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if !s.isSetupComplete() {
		t.Error("Expected setup to be marked complete")
	}
}

func TestSetupCORSHeaders(t *testing.T) {
	s, tmpDir := setupTestSetupServer(t)
	defer os.RemoveAll(tmpDir)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/setup/status", nil)
	req.Header.Set("Origin", "http://192.168.1.100")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	corsHeader := w.Header().Get("Access-Control-Allow-Origin")
	if corsHeader != "*" {
		t.Errorf("Expected CORS origin header '*' (AllowedOrigins=[*]), got %s", corsHeader)
	}
}
