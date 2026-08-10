package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flatrun/agent/internal/ai"
	"github.com/flatrun/agent/pkg/config"
	"github.com/gin-gonic/gin"
)

// countingProvider stands in for the model so the tests exercise the budget, the caps and the
// parsing rather than a network call. The package already has a stub for request shape; this
// one counts calls, which is what the budget assertions turn on.
type countingProvider struct {
	calls    int
	lastUser string
	reply    string
	err      error
}

func (p *countingProvider) Name() string { return "counting" }

func (p *countingProvider) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.calls++
	for _, m := range req.Messages {
		if m.Role == "user" {
			p.lastUser = m.Content
		}
	}
	if p.err != nil {
		return nil, p.err
	}
	return &ai.Response{Content: p.reply}, nil
}

func triageServer(t *testing.T, provider ai.Provider, dailyCap int) (*gin.Engine, *Server) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	server := &Server{
		pluginToken:  "plugin-secret",
		aiProvider:   provider,
		triageBudget: newTriageBudget(dailyCap),
		config:       &config.Config{},
	}
	router := gin.New()
	router.POST("/internal/ai/triage", server.triageLogIncident)
	return router, server
}

func triagePost(t *testing.T, router *gin.Engine, token string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/ai/triage", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Plugin-Token", token)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// The daily ceiling is what stops a bug in the funnel becoming a bill, so it has to hold at
// the endpoint rather than only in the app that calls it.
func TestTriageRefusesOnceTheDailyBudgetIsSpent(t *testing.T) {
	provider := &countingProvider{reply: `{"summary":"redis is unreachable","severity":"high","confidence":"medium"}`}
	router, _ := triageServer(t, provider, 2)

	body := map[string]any{"deployment": "shop", "sample": "connection refused", "count": 5}

	for i := 0; i < 2; i++ {
		if w := triagePost(t, router, "plugin-secret", body); w.Code != http.StatusOK {
			t.Fatalf("call %d should be allowed, got %d: %s", i+1, w.Code, w.Body.String())
		}
	}

	w := triagePost(t, router, "plugin-secret", body)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("the third call should be refused, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "budget") {
		t.Errorf("the refusal should say why, got %s", w.Body.String())
	}
	if provider.calls != 2 {
		t.Errorf("a refused triage must not reach the model, got %d calls", provider.calls)
	}
}

func TestTriageRequiresThePluginToken(t *testing.T) {
	provider := &countingProvider{reply: `{"summary":"x"}`}
	router, _ := triageServer(t, provider, 10)

	if w := triagePost(t, router, "", map[string]any{"sample": "x"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a token, got %d", w.Code)
	}
	if w := triagePost(t, router, "wrong", map[string]any{"sample": "x"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with the wrong token, got %d", w.Code)
	}
	if provider.calls != 0 {
		t.Errorf("an unauthorized call must not reach the model")
	}
}

// One line of a base64 payload can be bigger than forty ordinary ones, so the prompt is
// capped by size as well as by line count.
func TestTriagePromptIsCappedBySizeAndLines(t *testing.T) {
	provider := &countingProvider{reply: `{"summary":"ok"}`}
	router, _ := triageServer(t, provider, 10)

	context := make([]string, 500)
	for i := range context {
		context[i] = strings.Repeat("x", 500)
	}

	w := triagePost(t, router, "plugin-secret", map[string]any{
		"deployment": "shop",
		"sample":     "boom",
		"count":      3,
		"context":    context,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(provider.lastUser) > maxTriageContextChars+64 {
		t.Errorf("prompt should be capped near %d chars, got %d", maxTriageContextChars, len(provider.lastUser))
	}
}

// A model that wraps its JSON in a code fence is the common case, not an error.
func TestTriageReadsAFencedVerdict(t *testing.T) {
	provider := &countingProvider{reply: "```json\n{\"summary\":\"disk is full\",\"next_step\":\"free space\"}\n```"}
	router, _ := triageServer(t, provider, 10)

	w := triagePost(t, router, "plugin-secret", map[string]any{"deployment": "shop", "sample": "no space left"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Triage triageResponse `json:"triage"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Triage.Summary != "disk is full" {
		t.Errorf("expected the fenced verdict to be read, got %+v", resp.Triage)
	}
}

// A reply that is not a verdict is an error, not an incident annotated with prose.
func TestTriageRejectsANonVerdictReply(t *testing.T) {
	provider := &countingProvider{reply: "I would need to see the source code to say."}
	router, _ := triageServer(t, provider, 10)

	w := triagePost(t, router, "plugin-secret", map[string]any{"deployment": "shop", "sample": "boom"})
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for an unusable reply, got %d: %s", w.Code, w.Body.String())
	}
}

func TestTriageWithoutAProviderIsUnavailable(t *testing.T) {
	router, _ := triageServer(t, nil, 10)
	if w := triagePost(t, router, "plugin-secret", map[string]any{"sample": "x"}); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when the assistant is not configured, got %d", w.Code)
	}
}

// The budget rolls over rather than being spent forever.
func TestTriageBudgetResetsDaily(t *testing.T) {
	b := newTriageBudget(1)
	day1 := time.Date(2026, 8, 6, 23, 59, 0, 0, time.UTC)
	if _, _, ok := b.take(day1); !ok {
		t.Fatal("the first call of the day should be allowed")
	}
	if _, _, ok := b.take(day1); ok {
		t.Fatal("the second call should be refused")
	}
	day2 := time.Date(2026, 8, 7, 0, 1, 0, 0, time.UTC)
	if _, _, ok := b.take(day2); !ok {
		t.Fatal("the next day should start fresh")
	}
}

func TestParseTriageVerdictRequiresASummary(t *testing.T) {
	if _, err := parseTriageVerdict(`{"cause":"something"}`); err == nil {
		t.Fatal("a verdict with no summary is not usable")
	}
	if _, err := parseTriageVerdict(fmt.Sprintf(`{"summary":%q}`, "ok")); err != nil {
		t.Fatalf("a verdict with a summary should parse, got %v", err)
	}
}
