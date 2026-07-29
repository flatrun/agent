package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/pkg/updater"
	"github.com/gin-gonic/gin"
)

func TestParseChannel(t *testing.T) {
	tests := []struct {
		in   string
		want updater.Channel
	}{
		{"prerelease", updater.ChannelPrerelease},
		{"stable", updater.ChannelStable},
		{"", updater.ChannelStable},
		{"garbage", updater.ChannelStable},
	}
	for _, tt := range tests {
		if got := parseChannel(tt.in); got != tt.want {
			t.Errorf("parseChannel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// A malformed body must be rejected before any release is fetched or installed,
// so an update is never attempted on an unparseable request.
func TestTriggerAgentUpdateRejectsBadBodyBeforeUpdating(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := &Server{}
	router := gin.New()
	router.Use(actorMiddleware(testActor(auth.RoleAdmin, nil)))
	router.POST("/agent/update", server.triggerAgentUpdate)

	req := httptest.NewRequest(http.MethodPost, "/agent/update", bytes.NewBufferString("{invalid"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 before update, got %d: %s", w.Code, w.Body.String())
	}
}
