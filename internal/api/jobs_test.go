package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// fakeRunner stands in for the docker-backed action runner so tests exercise
// the job lifecycle over HTTP without shelling out to compose.
type fakeRunner struct {
	lines  []string
	err    error
	gate   chan struct{}
	optsCh chan actionOptions
}

func (f *fakeRunner) run(_, _ string, opts actionOptions, emit func(string)) error {
	if f.optsCh != nil {
		f.optsCh <- opts
	}
	for _, l := range f.lines {
		emit(l)
	}
	if f.gate != nil {
		<-f.gate
	}
	return f.err
}

func newJobTestServer(runner *fakeRunner) *Server {
	gin.SetMode(gin.TestMode)
	s := &Server{jobs: newJobRegistry()}
	s.runDeploymentAction = runner.run
	s.runServiceAction = func(action, name, service string, opts actionOptions, emit func(string)) error {
		return runner.run(action, name, opts, emit)
	}
	return s
}

func newJobRouter(s *Server) *gin.Engine {
	r := gin.New()
	r.POST("/api/deployments/:name/start", s.startDeployment)
	r.POST("/api/deployments/:name/stop", s.stopDeployment)
	r.POST("/api/deployments/:name/restart", s.restartDeployment)
	r.POST("/api/deployments/:name/services/:service/job", s.enqueueServiceJob)
	r.GET("/api/deployments/:name/jobs/active", s.getActiveDeploymentJob)
	r.GET("/api/deployments/:name/jobs/:jobId", s.getDeploymentJob)
	return r
}

func enqueue(t *testing.T, srvURL, name, action string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(srvURL+"/api/deployments/"+name+"/"+action, "application/json", nil)
	if err != nil {
		t.Fatalf("%s request failed: %v", action, err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func pollJob(t *testing.T, srvURL, name, jobID string) JobSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(srvURL + "/api/deployments/" + name + "/jobs/" + jobID)
		if err != nil {
			t.Fatalf("status request failed: %v", err)
		}
		var snap JobSnapshot
		_ = json.NewDecoder(resp.Body).Decode(&snap)
		resp.Body.Close()
		if snap.Status == JobSucceeded || snap.Status == JobFailed {
			return snap
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish in time", jobID)
	return JobSnapshot{}
}

func TestDeploymentActionJobLifecycle(t *testing.T) {
	s := newJobTestServer(&fakeRunner{lines: []string{"Pulling nginx", "Creating web", "Started"}})
	srv := newSkippableHTTPServer(t, newJobRouter(s))
	defer srv.Close()

	code, body := enqueue(t, srv.URL, "myapp", "start")
	if code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", code)
	}
	jobID, _ := body["job_id"].(string)
	if jobID == "" {
		t.Fatal("expected a job_id in the response")
	}

	snap := pollJob(t, srv.URL, "myapp", jobID)
	if snap.Status != JobSucceeded {
		t.Fatalf("expected succeeded, got %s (err=%s)", snap.Status, snap.Error)
	}
	if !strings.Contains(snap.Output, "Pulling nginx") || !strings.Contains(snap.Output, "Started") {
		t.Fatalf("expected streamed output to be buffered, got %q", snap.Output)
	}

	// Reload survival: the finished job is still queryable.
	snap2 := pollJob(t, srv.URL, "myapp", jobID)
	if snap2.Status != JobSucceeded || snap2.Output != snap.Output {
		t.Fatalf("finished job should remain queryable with the same output")
	}
}

func TestDeploymentActionJobFailureSurfaces(t *testing.T) {
	s := newJobTestServer(&fakeRunner{lines: []string{"boom"}, err: errStub("compose failed")})
	srv := newSkippableHTTPServer(t, newJobRouter(s))
	defer srv.Close()

	_, body := enqueue(t, srv.URL, "myapp", "restart")
	jobID, _ := body["job_id"].(string)

	snap := pollJob(t, srv.URL, "myapp", jobID)
	if snap.Status != JobFailed {
		t.Fatalf("expected failed, got %s", snap.Status)
	}
	if !strings.Contains(snap.Error, "compose failed") {
		t.Fatalf("expected error to surface, got %q", snap.Error)
	}
}

func TestDeploymentActionRejectsConcurrent(t *testing.T) {
	gate := make(chan struct{})
	s := newJobTestServer(&fakeRunner{lines: []string{"working"}, gate: gate})
	srv := newSkippableHTTPServer(t, newJobRouter(s))
	defer srv.Close()

	code, body := enqueue(t, srv.URL, "myapp", "start")
	if code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", code)
	}
	firstID, _ := body["job_id"].(string)

	// Wait until the first job is registered as active.
	if !waitFor(func() bool { return s.jobs.activeFor("myapp") != nil }) {
		t.Fatal("first job never became active")
	}

	code2, body2 := enqueue(t, srv.URL, "myapp", "stop")
	if code2 != http.StatusConflict {
		t.Fatalf("expected 409 for concurrent action, got %d", code2)
	}
	if body2["active_job_id"] != firstID {
		t.Fatalf("expected active_job_id %s, got %v", firstID, body2["active_job_id"])
	}

	close(gate)
	snap := pollJob(t, srv.URL, "myapp", firstID)
	if snap.Status != JobSucceeded {
		t.Fatalf("expected first job to succeed after gate released, got %s", snap.Status)
	}

	// A new action is accepted once the deployment is free again.
	code3, _ := enqueue(t, srv.URL, "myapp", "stop")
	if code3 != http.StatusAccepted {
		t.Fatalf("expected 202 after the first job finished, got %d", code3)
	}
}

func TestServiceRestartJobIsScopedPerService(t *testing.T) {
	gate := make(chan struct{})
	s := newJobTestServer(&fakeRunner{lines: []string{"Restarting web"}, gate: gate})
	srv := newSkippableHTTPServer(t, newJobRouter(s))
	defer srv.Close()

	post := func(service string) (int, map[string]any) {
		resp, err := http.Post(
			srv.URL+"/api/deployments/app/services/"+service+"/job",
			"application/json",
			strings.NewReader(`{"action":"restart"}`),
		)
		if err != nil {
			t.Fatalf("service restart request failed: %v", err)
		}
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}

	code, body := post("web")
	if code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", code)
	}
	webJobID, _ := body["job_id"].(string)
	if !waitFor(func() bool { return s.jobs.get(webJobID) != nil }) {
		t.Fatal("web job not registered")
	}

	// Same service while running is rejected.
	if code2, _ := post("web"); code2 != http.StatusConflict {
		t.Fatalf("expected 409 for the same service, got %d", code2)
	}

	// A different service runs concurrently.
	if code3, _ := post("db"); code3 != http.StatusAccepted {
		t.Fatalf("expected 202 for a different service, got %d", code3)
	}

	close(gate)
	snap := pollJob(t, srv.URL, "app", webJobID)
	if snap.Status != JobSucceeded || snap.Service != "web" {
		t.Fatalf("expected succeeded web job, got status=%s service=%s", snap.Status, snap.Service)
	}
}

// Effective-apply flags in the request body must reach the runner so updated env vars
// and images take effect.
func TestServiceJobThreadsEffectiveApplyOptions(t *testing.T) {
	optsCh := make(chan actionOptions, 1)
	s := newJobTestServer(&fakeRunner{lines: []string{"ok"}, optsCh: optsCh})
	srv := newSkippableHTTPServer(t, newJobRouter(s))
	defer srv.Close()

	resp, err := http.Post(
		srv.URL+"/api/deployments/app/services/web/job",
		"application/json",
		strings.NewReader(`{"action":"rebuild","force_recreate":true,"no_cache":true,"fresh_pull":true}`),
	)
	if err != nil {
		t.Fatalf("service job request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	select {
	case got := <-optsCh:
		if !got.ForceRecreate || !got.NoCache || !got.FreshPull {
			t.Fatalf("runner received opts %+v, want all flags set", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner was not invoked with options")
	}
}

func TestDeploymentJobStreamReplaysAndCompletes(t *testing.T) {
	s := &Server{
		jobs:           newJobRegistry(),
		authMiddleware: auth.NewMiddleware(&config.AuthConfig{Enabled: false}),
	}

	// Pre-populate a finished job so the stream must replay buffered output.
	job, _ := s.jobs.create("myapp", "start", actionOptions{})
	job.appendLine("Pulling nginx")
	job.appendLine("Started")
	s.jobs.finish(job, JobSucceeded, "")

	r := gin.New()
	r.GET("/api/deployments/:name/jobs/:jobId/stream", s.streamDeploymentJob)
	srv := newSkippableHTTPServer(t, r)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/deployments/myapp/jobs/" + job.ID() + "/stream"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	defer conn.Close()

	var lines []string
	var result map[string]any
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var frame map[string]any
		if json.Unmarshal(msg, &frame) != nil {
			continue
		}
		switch frame["type"] {
		case "line":
			if d, ok := frame["data"].(string); ok {
				lines = append(lines, d)
			}
		case "result":
			result = frame
		}
		if result != nil {
			break
		}
	}

	if len(lines) != 2 || lines[0] != "Pulling nginx" || lines[1] != "Started" {
		t.Fatalf("expected replayed lines, got %v", lines)
	}
	if result == nil || result["status"] != string(JobSucceeded) {
		t.Fatalf("expected a succeeded result frame, got %v", result)
	}
}

func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

type errStub string

func (e errStub) Error() string { return string(e) }
