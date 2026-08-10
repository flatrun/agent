package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flatrun/agent/internal/infra"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
)

func systemLogsRouter(cfg *config.Config, register func(*gin.Engine, *Server)) *gin.Engine {
	gin.SetMode(gin.TestMode)
	server := &Server{infraManager: infra.NewManager(cfg)}
	router := gin.New()
	register(router, server)
	return router
}

// The proxy writes its access log to stdout and its error log to stderr, so the two are
// offered apart; only the access log carries the host a request asked for, which is the one
// thing that can be matched back to a deployment.
func TestSystemLogSourcesSplitNginxAccessFromError(t *testing.T) {
	cfg := &config.Config{}
	cfg.Nginx.Enabled = true
	cfg.Nginx.ContainerName = "flatrun-nginx"
	cfg.Infrastructure.Redis.Enabled = true
	cfg.Infrastructure.Redis.Container = "flatrun-redis"

	router := systemLogsRouter(cfg, func(r *gin.Engine, s *Server) {
		r.GET("/system/logs/sources", s.listSystemLogSources)
	})

	req := httptest.NewRequest(http.MethodGet, "/system/logs/sources", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Sources []systemLogSource `json:"sources"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	byID := map[string]systemLogSource{}
	for _, src := range resp.Sources {
		byID[src.ID] = src
	}

	access, ok := byID["nginx-access"]
	if !ok {
		t.Fatalf("no access log source offered: %+v", resp.Sources)
	}
	if access.Stream != infra.LogStreamStdout || !access.ByDeployment {
		t.Errorf("the access log should be readable per deployment from stdout, got %+v", access)
	}

	errSrc, ok := byID["nginx-error"]
	if !ok {
		t.Fatalf("no error log source offered: %+v", resp.Sources)
	}
	if errSrc.Stream != infra.LogStreamStderr || errSrc.ByDeployment {
		t.Errorf("the error log says nothing about which deployment a line is from, got %+v", errSrc)
	}

	if _, ok := byID["redis"]; !ok {
		t.Errorf("shared infrastructure should be readable too: %+v", resp.Sources)
	}
}

// Asking to see one deployment's requests in a log that never records the host would hand
// back an arbitrary subset, so it is refused rather than silently ignored.
func TestSystemLogsRejectDeploymentFilterOnASourceThatCannotAnswerIt(t *testing.T) {
	cfg := &config.Config{}
	cfg.Nginx.Enabled = true
	cfg.Nginx.ContainerName = "flatrun-nginx"

	router := systemLogsRouter(cfg, func(r *gin.Engine, s *Server) {
		r.GET("/system/logs", s.getSystemLogs)
	})

	req := httptest.NewRequest(http.MethodGet, "/system/logs?source=nginx-error&deployment=shop", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSystemLogsRejectUnknownSource(t *testing.T) {
	cfg := &config.Config{}
	cfg.Nginx.Enabled = true
	cfg.Nginx.ContainerName = "flatrun-nginx"

	router := systemLogsRouter(cfg, func(r *gin.Engine, s *Server) {
		r.GET("/system/logs", s.getSystemLogs)
	})

	req := httptest.NewRequest(http.MethodGet, "/system/logs?source=not-a-source", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
