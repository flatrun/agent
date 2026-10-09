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
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

type recordingAccessSender struct {
	calls     int
	targetID  string
	recipient string
	message   string
}

func (s *recordingAccessSender) SendEmailTo(targetID, recipient string, message notify.Notification) error {
	s.calls++
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
        - "@flatrun.dev"
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
	router.POST("/api/access/verify", server.confirmApplicationAccess)

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
		"email": {"visitor@flatrun.dev"}, "return": {"/"},
	}.Encode()))
	request.Host = "private.example.com"
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || sender.recipient != "visitor@flatrun.dev" || sender.message == "" {
		t.Fatalf("domain access request = %d, recipient = %q, message = %q", response.Code, sender.recipient, sender.message)
	}
	sender.message = ""
	request = httptest.NewRequest(http.MethodPost, "/api/access/request", strings.NewReader(url.Values{
		"email": {"other@example.com"}, "return": {"/"},
	}.Encode()))
	request.Host = "private.example.com"
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || sender.message != "" || sender.calls != 2 {
		t.Fatalf("unlisted email response = %d, message = %q", response.Code, sender.message)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/access/verify?"+parsed.RawQuery, nil)
	request.Host = "other.example.com"
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-host verification response = %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/access/verify?"+parsed.RawQuery, nil)
	request.Host = "private.example.com"
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(response.Result().Cookies()) != 0 || !strings.Contains(response.Body.String(), "Confirm sign-in") {
		t.Fatalf("verification preview response = %d, cookies = %v", response.Code, response.Result().Cookies())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/access/verify?"+parsed.RawQuery, nil)
	request.Host = "private.example.com"
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("repeated link preview response = %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/access/verify", strings.NewReader(url.Values{"token": {parsed.Query().Get("token")}}.Encode()))
	request.Host = "private.example.com"
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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

func TestWordPressAccessRequiresAllowlistedEmailBeforeSending(t *testing.T) {
	for _, test := range []struct {
		name, mode, email string
		allowed           []string
		status, emails    int
	}{
		{"unknown address", "allowlist", "unknown@example.com", []string{"person@example.com"}, http.StatusForbidden, 0},
		{"legacy open policy", "any_verified", "unknown@example.com", nil, http.StatusForbidden, 0},
		{"legacy policy with allowlist", "any_verified", "unknown@example.com", []string{"person@example.com"}, http.StatusForbidden, 0},
		{"recognized address", "allowlist", "person@example.com", []string{"person@example.com"}, http.StatusAccepted, 1},
		{"recognized domain", "allowlist", "person@example.com", []string{"@example.com"}, http.StatusAccepted, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			createTestDeployment(t, base, "wordpress-site", &models.ServiceMetadata{
				Name: "wordpress-site", Type: "wordpress", Domains: []models.DomainConfig{{
					ID: "login", Service: "wordpress", ContainerPort: 80, Domain: "wordpress.example.com", PathPrefix: "/wp-login.php",
					Access: &models.DomainAccessConfig{Enabled: true, Mode: test.mode, AllowedEmails: test.allowed, EmailTargetID: "smtp"},
				}},
			})
			service, err := access.New(base)
			if err != nil {
				t.Fatal(err)
			}
			sender := &recordingAccessSender{}
			server := &Server{manager: docker.NewManager(base), access: service, accessEmailSender: sender}
			router := gin.New()
			router.POST("/api/access/request", server.requestApplicationAccess)
			router.GET("/api/access/check", server.checkApplicationAccess)
			request := httptest.NewRequest(http.MethodPost, "/api/access/request", strings.NewReader(url.Values{
				"email": {test.email}, "return": {"/wp-login.php"},
			}.Encode()))
			request.Host = "wordpress.example.com"
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.status || sender.calls != test.emails {
				t.Fatalf("status = %d, email attempts = %d", response.Code, sender.calls)
			}
			session, err := service.Session("unknown@other.example", "wordpress.example.com", 24)
			if err != nil {
				t.Fatal(err)
			}
			request = httptest.NewRequest(http.MethodGet, "/api/access/check", nil)
			request.Header.Set("X-Original-Host", "wordpress.example.com")
			request.Header.Set("X-Original-URI", "/wp-login.php")
			request.AddCookie(&http.Cookie{Name: access.CookieName, Value: session})
			response = httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("unlisted existing session response = %d", response.Code)
			}
		})
	}
}

func TestWordPressDomainAccessRejectsOpenPolicyThroughHTTP(t *testing.T) {
	base := t.TempDir()
	createTestDeployment(t, base, "wordpress-site", &models.ServiceMetadata{
		Name: "wordpress-site", Type: "wordpress", Domains: []models.DomainConfig{{
			ID: "login", Service: "web", ContainerPort: 80, Domain: "wordpress.example.com", PathPrefix: "/wp-login.php",
		}},
	})
	server := &Server{manager: docker.NewManager(base)}
	router := gin.New()
	router.POST("/deployments/:name/domains", server.addDomain)
	router.PUT("/deployments/:name/domains/:domainId", server.updateDomain)
	router.PUT("/deployments/:name/metadata", server.updateDeploymentMetadata)
	domain := `{"id":"login","service":"web","domain":"wordpress.example.com","path_prefix":"/wp-login.php","access":{"enabled":true,"mode":"any_verified","email_target_id":"smtp"}}`
	for _, requestCase := range []struct{ method, path, body string }{
		{http.MethodPost, "/deployments/wordpress-site/domains", domain},
		{http.MethodPut, "/deployments/wordpress-site/domains/login", domain},
		{http.MethodPut, "/deployments/wordpress-site/metadata", `{"domains":[` + domain + `]}`},
	} {
		request := httptest.NewRequest(requestCase.method, requestCase.path, strings.NewReader(requestCase.body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "WordPress access requires an email allowlist") {
			t.Fatalf("%s: status = %d, body = %s", requestCase.path, response.Code, response.Body.String())
		}
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
	for _, expected := range []string{"FlatRun", "Protected by FlatRun", accessLogo, "Verify your email"} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("access page is missing %q", expected)
		}
	}
}

func TestApplicationAccessDenialUsesFlatRunBranding(t *testing.T) {
	server := &Server{}
	router := gin.New()
	router.POST("/api/access/request", server.requestApplicationAccess)
	request := httptest.NewRequest(http.MethodPost, "/api/access/request", strings.NewReader(url.Values{
		"email": {"person@example.com"}, "return": {"/"},
	}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("confirmation response = %d", response.Code)
	}
	for _, expected := range []string{"FlatRun", "Protected by FlatRun", accessLogo, "Access denied"} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("confirmation page is missing %q", expected)
		}
	}
}
