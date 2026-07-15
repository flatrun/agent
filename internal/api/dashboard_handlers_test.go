package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flatrun/agent/internal/dashboards"
	"github.com/gin-gonic/gin"
)

func dashboardRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	s := &Server{dashboards: dashboards.NewStore(t.TempDir())}
	r := gin.New()
	r.GET("/dashboards", s.listDashboards)
	r.GET("/dashboards/:id", s.getDashboard)
	r.POST("/dashboards", s.saveDashboard)
	r.DELETE("/dashboards/:id", s.deleteDashboard)
	return r
}

func TestDashboardLifecycleThroughTheAPI(t *testing.T) {
	r := dashboardRouter(t)

	body := `{"name":"Shop","panels":[
		{"title":"CPU","source":"container","series":"container.cpu.usage","type":"line","width":6},
		{"title":"Requests","source":"serving","series":"requests","deployment":"shop","type":"line","width":6}
	]}`
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/dashboards", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body.String())
	}

	var created dashboards.Dashboard
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || len(created.Panels) != 2 || created.Panels[0].ID == "" {
		t.Fatalf("created = %+v", created)
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboards", nil))
	var list struct {
		Dashboards []dashboards.Dashboard `json:"dashboards"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Dashboards) != 1 {
		t.Fatalf("list = %+v", list.Dashboards)
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboards/"+created.ID, nil))
	if rec.Code != http.StatusOK {
		t.Errorf("get status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/dashboards/"+created.ID, nil))
	if rec.Code != http.StatusOK {
		t.Errorf("delete status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboards/"+created.ID, nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", rec.Code)
	}
}

func TestDashboardRejectsAPanelThatCannotDraw(t *testing.T) {
	r := dashboardRouter(t)

	body := `{"name":"Bad","panels":[{"title":"Disk","source":"container","series":"container.disk.usage","type":"line","width":6}]}`
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/dashboards", strings.NewReader(body)))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestDashboardsEmptyList(t *testing.T) {
	r := dashboardRouter(t)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboards", nil))

	// An empty list serializes as an array, not null, so the UI can iterate it as it stands.
	if body := strings.TrimSpace(rec.Body.String()); body != `{"dashboards":[]}` {
		t.Errorf("body = %s", body)
	}
}
