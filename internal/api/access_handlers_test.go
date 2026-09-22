package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flatrun/agent/internal/access"
	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/docker"
	"github.com/flatrun/agent/internal/notify"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
)

type recordingAccessSender struct {
	targetID  string
	recipient string
	message   string
}

func (s *recordingAccessSender) SendEmailTo(targetID, recipient string, message notify.Notification) error {
	s.targetID = targetID
	s.recipient = recipient
	s.message = message.Message
	return nil
}

func TestApplicationAccessCheckUsesTheVisitorHTTPBoundary(t *testing.T) {
	base := t.TempDir()
	deploymentPath := filepath.Join(base, "private-app")
	if err := os.MkdirAll(deploymentPath, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := `name: private-app
type: web
domains:
  - id: private
    service: web
    container_port: 80
    domain: private.example.com
    access:
      enabled: true
      mode: allowlist
      allowed_emails:
        - person@example.com
      email_target_id: smtp
`
	if err := os.WriteFile(filepath.Join(deploymentPath, "service.yml"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deploymentPath, "docker-compose.yml"), []byte("services:\n  web:\n    image: nginx:alpine\n"), 0644); err != nil {
		t.Fatal(err)
	}
	accessService, err := access.New(base)
	if err != nil {
		t.Fatal(err)
	}
	sender := &recordingAccessSender{}
	server := &Server{manager: docker.NewManager(base), access: accessService, accessEmailSender: sender}
	router := gin.New()
	router.GET("/api/access/check", server.checkApplicationAccess)
	router.POST("/api/access/request", server.requestApplicationAccess)
	router.GET("/api/access/verify", server.verifyApplicationAccess)

	request := httptest.NewRequest(http.MethodGet, "/api/access/check", nil)
	request.Header.Set("X-Original-Host", "private.example.com")
	request.Header.Set("X-Original-URI", "/")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous response = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/access/request", strings.NewReader(url.Values{
		"email": {"person@example.com"}, "return": {"/"},
	}.Encode()))
	request.Host = "private.example.com"
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Forwarded-Proto", "https")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || sender.targetID != "smtp" || sender.recipient != "person@example.com" {
		t.Fatalf("access request = %d, target = %q, recipient = %q", response.Code, sender.targetID, sender.recipient)
	}
	link := strings.TrimPrefix(sender.message, "Open this link to continue: ")
	parsed, err := url.Parse(link)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "private.example.com" {
		t.Fatalf("access link = %q, error = %v", link, err)
	}
	sender.message = ""
	request = httptest.NewRequest(http.MethodPost, "/api/access/request", strings.NewReader(url.Values{
		"email": {"other@example.com"}, "return": {"/"},
	}.Encode()))
	request.Host = "private.example.com"
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || sender.message != "" {
		t.Fatalf("unlisted email response = %d, message = %q", response.Code, sender.message)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/access/verify?"+parsed.RawQuery, nil)
	request.Host = "private.example.com"
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusFound || len(response.Result().Cookies()) != 1 {
		t.Fatalf("verification response = %d, cookies = %v", response.Code, response.Result().Cookies())
	}
	cookie := response.Result().Cookies()[0]
	request = httptest.NewRequest(http.MethodGet, "/api/access/check", nil)
	request.Header.Set("X-Original-Host", "private.example.com")
	request.Header.Set("X-Original-URI", "/")
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("verified response = %d, body = %s", response.Code, response.Body.String())
	}
	restarted, err := access.New(base)
	if err != nil {
		t.Fatal(err)
	}
	server.access = restarted
	request = httptest.NewRequest(http.MethodGet, "/api/access/verify?"+parsed.RawQuery, nil)
	request.Host = "private.example.com"
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("replayed verification response = %d", response.Code)
	}
	if err := os.WriteFile(filepath.Join(deploymentPath, "service.yml"), []byte("domains: [invalid"), 0644); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/access/check", nil)
	request.Header.Set("X-Original-Host", "private.example.com")
	request.Header.Set("X-Original-URI", "/")
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unreadable policy response = %d", response.Code)
	}
}

func TestAccessEmailTargetsRespectDeploymentGrants(t *testing.T) {
	base := t.TempDir()
	cfg := &config.Config{Auth: config.AuthConfig{Enabled: true, JWTSecret: "access-test-secret"}}
	t.Setenv("FLATRUN_ADMIN_PASSWORD", "testadminpass")
	authManager, err := auth.NewManager(base, &cfg.Auth, true)
	if err != nil {
		t.Fatal(err)
	}
	defer authManager.Close()
	notifications := notify.NewService(base)
	if err := notifications.Save(notify.Config{Targets: []notify.Target{
		{ID: "smtp", Name: "Mail", URL: "smtp://mail.example/?from=ops%40example.com", Enabled: true},
		{ID: "webhook", Name: "Webhook", URL: "generic+https://example.com", Enabled: true},
		{ID: "disabled", Name: "Disabled", URL: "smtp://mail.example/", Enabled: false},
	}}); err != nil {
		t.Fatal(err)
	}
	server := &Server{notify: notifications}
	middleware := auth.NewMiddlewareWithManager(&cfg.Auth, authManager)
	router := gin.New()
	protected := router.Group("/api", middleware.RequireAuth())
	protected.GET("/deployments/:name/access/email-targets", middleware.RequirePermission(auth.PermDeploymentsWrite), middleware.RequireDeploymentAccess(auth.AccessLevelWrite), server.getAccessEmailTargets)
	shopKey := objectStoreKey(t, &Server{authManager: authManager}, "access-shop-key", []string{auth.PermDeploymentsWrite.String()}, auth.DeploymentAccess{"shop": auth.AccessLevelWrite})
	otherKey := objectStoreKey(t, &Server{authManager: authManager}, "access-other-key", []string{auth.PermDeploymentsWrite.String()}, auth.DeploymentAccess{"other": auth.AccessLevelWrite})
	readerKey := objectStoreKey(t, &Server{authManager: authManager}, "access-reader-key", []string{auth.PermDeploymentsRead.String()}, auth.DeploymentAccess{"shop": auth.AccessLevelWrite})

	response := osReq(t, router, http.MethodGet, "/api/deployments/shop/access/email-targets", shopKey, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("shop selector = %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Targets []map[string]string `json:"targets"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Targets) != 1 || body.Targets[0]["id"] != "smtp" || body.Targets[0]["name"] != "Mail" || len(body.Targets[0]) != 2 {
		t.Fatalf("selector exposed unexpected targets: %s", response.Body.String())
	}
	response = osReq(t, router, http.MethodGet, "/api/deployments/shop/access/email-targets", otherKey, nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("other deployment selector = %d", response.Code)
	}
	response = osReq(t, router, http.MethodGet, "/api/deployments/shop/access/email-targets", readerKey, nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("read-only selector = %d", response.Code)
	}
}

func TestAccessEmailTargetsAreEmptyWithoutNotificationService(t *testing.T) {
	server := &Server{}
	router := gin.New()
	router.GET("/api/deployments/:name/access/email-targets", server.getAccessEmailTargets)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/deployments/shop/access/email-targets", nil))
	if response.Code != http.StatusOK || response.Body.String() != "{\"targets\":[]}" {
		t.Fatalf("selector response = %d: %s", response.Code, response.Body.String())
	}
}

func TestApplicationAccessLoginRejectsUnsafeReturnPath(t *testing.T) {
	server := &Server{}
	router := gin.New()
	router.GET("/api/access/login", server.applicationAccessLogin)
	request := httptest.NewRequest(http.MethodGet, "/api/access/login?return=/%5Cother.example.com", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `name="return" value="/"`) {
		t.Fatalf("unsafe return path was accepted: %d %s", response.Code, response.Body.String())
	}
}
