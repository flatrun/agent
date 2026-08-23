package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/contextkeys"
	"github.com/flatrun/agent/internal/notify"
	"github.com/gin-gonic/gin"
)

func setupNotifyTest(t *testing.T) (*Server, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	s := &Server{
		notify:      notify.NewService(t.TempDir()),
		pluginToken: "plugin-token",
	}
	r := gin.New()
	r.GET("/notifications/targets", s.getNotificationTargets)
	r.GET("/alerts/target-options", s.getAlertTargetOptions)
	r.GET("/notifications/incidents", s.listNotificationIncidents)
	r.GET("/notifications/rules", s.listNotificationRules)
	r.PUT("/notifications/rules", s.updateNotificationRules)
	r.PUT("/notifications/targets", s.updateNotificationTargets)
	r.POST("/notifications/test", s.testNotification)
	r.POST("/internal/notify/emit", s.emitNotification)
	r.POST("/internal/events", s.emitEvent)
	return s, r
}

func TestAlertTargetOptionsExposeOnlyEnabledNames(t *testing.T) {
	s, r := setupNotifyTest(t)
	if err := s.notify.Save(notify.Config{Targets: []notify.Target{
		{ID: "ops", Name: "Ops", URL: "generic+https://example.com/secret", Enabled: true},
		{ID: "off", Name: "Disabled", URL: "smtp://user:pass@example.com", Enabled: false},
	}}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/alerts/target-options", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); body != `{"targets":[{"id":"ops","name":"Ops"}]}` {
		t.Fatalf("body = %s", body)
	}
}

func TestNotificationRulesRoundTripThroughHTTP(t *testing.T) {
	_, r := setupNotifyTest(t)
	payload := `{"rules":[{"id":"critical-fleet","name":"Critical fleet incidents","enabled":true,"topics":["fleet"],"severities":["critical"],"target_ids":["email"]}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/notifications/rules", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/notifications/rules", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"critical-fleet"`) {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestEmitEventCorrelatesIncidentThroughHTTP(t *testing.T) {
	_, r := setupNotifyTest(t)
	emit := func(payload string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/internal/events", bytes.NewBufferString(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Plugin-Token", "plugin-token")
		r.ServeHTTP(w, req)
		return w
	}

	first := emit(`{"source":"fleet","type":"node.unavailable","severity":"critical","title":"prod2 unavailable","scope":{"node":"prod2"}}`)
	if first.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", first.Code, first.Body.String())
	}
	second := emit(`{"source":"capacity","type":"deployment.unavailable","severity":"critical","title":"app unavailable","scope":{"node":"prod2","deployment":"app"},"correlation_key":"node:prod2"}`)
	if second.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", second.Code, second.Body.String())
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/notifications/incidents", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"event_count":2`) {
		t.Fatalf("incidents status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestGetNotificationTargetsMasksSecret(t *testing.T) {
	s, r := setupNotifyTest(t)
	if err := s.notify.Save(notify.Config{Targets: []notify.Target{
		{ID: "1", Name: "email", URL: "smtp://user:secret@host:587", Enabled: true},
	}}); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/notifications/targets", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "secret") {
		t.Errorf("response leaked the target credential: %s", body)
	}
	if !strings.Contains(body, notify.MaskedURL) {
		t.Errorf("response should mask the target URL: %s", body)
	}
	if !strings.Contains(body, `"kind":"email"`) {
		t.Errorf("response should identify the safe target kind: %s", body)
	}
}

func TestNotificationTargetByIDThroughHTTP(t *testing.T) {
	s, r := setupNotifyTest(t)
	if err := s.notify.Save(notify.Config{Targets: []notify.Target{
		{ID: "ops", Name: "Operations", URL: "generic+https://example.com/hook", Enabled: false},
	}}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/notifications/test", strings.NewReader(`{"target_id":"ops"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "target is disabled") {
		t.Fatalf("saved target was not selected: %s", w.Body.String())
	}
}

func TestUpdateNotificationTargetsPreservesMaskedSecret(t *testing.T) {
	s, r := setupNotifyTest(t)
	const real = "smtp://user:secret@host:587"
	if err := s.notify.Save(notify.Config{Targets: []notify.Target{
		{ID: "1", Name: "email", URL: real, Enabled: true},
	}}); err != nil {
		t.Fatal(err)
	}

	payload, _ := json.Marshal(notify.Config{Targets: []notify.Target{
		{ID: "1", Name: "renamed", URL: notify.MaskedURL, Enabled: true},
	}})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/notifications/targets", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	stored := s.notify.Load()
	if len(stored.Targets) != 1 || stored.Targets[0].URL != real {
		t.Errorf("saving a masked URL must keep the stored secret, got %+v", stored.Targets)
	}
	if stored.Targets[0].Name != "renamed" {
		t.Errorf("name change should apply, got %q", stored.Targets[0].Name)
	}
}

func TestEmitNotificationRequiresPluginToken(t *testing.T) {
	_, r := setupNotifyTest(t)
	body := func() *bytes.Reader { return bytes.NewReader([]byte(`{"title":"t","message":"m"}`)) }

	cases := []struct {
		name   string
		token  string
		status int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"wrong token", "nope", http.StatusUnauthorized},
		{"correct token", "plugin-token", http.StatusAccepted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/internal/notify/emit", body())
			req.Header.Set("Content-Type", "application/json")
			if tc.token != "" {
				req.Header.Set("X-Plugin-Token", tc.token)
			}
			r.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Errorf("status = %d, want %d", w.Code, tc.status)
			}
		})
	}
}

func TestToolAllowedGlobalWrite(t *testing.T) {
	s := &Server{}
	ctx := func(actor *auth.ActorContext) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		if actor != nil {
			c.Set(contextkeys.Actor, actor)
		}
		return c
	}

	if err := s.toolAllowedGlobalWrite(ctx(&auth.ActorContext{Role: auth.RoleAdmin})); err != nil {
		t.Errorf("admin should be allowed: %v", err)
	}
	if err := s.toolAllowedGlobalWrite(ctx(&auth.ActorContext{
		Role:        auth.RoleViewer,
		Permissions: []string{string(auth.PermSettingsWrite)},
	})); err != nil {
		t.Errorf("actor with settings-write should be allowed: %v", err)
	}
	if err := s.toolAllowedGlobalWrite(ctx(&auth.ActorContext{Role: auth.RoleViewer})); err == nil {
		t.Error("actor without settings-write should be denied")
	}
	if err := s.toolAllowedGlobalWrite(ctx(nil)); err != nil {
		t.Errorf("nil actor (auth disabled) should be allowed: %v", err)
	}
}
