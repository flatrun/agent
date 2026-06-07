package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flatrun/agent/pkg/config"
)

func TestNewDisabled(t *testing.T) {
	if _, err := New(&config.AIConfig{Enabled: false}); err != ErrDisabled {
		t.Errorf("err = %v, want ErrDisabled", err)
	}
	if _, err := New(nil); err != ErrDisabled {
		t.Errorf("nil cfg err = %v, want ErrDisabled", err)
	}
}

func TestRedactor(t *testing.T) {
	r := NewRedactor([]string{"hunter2secret", "short", "  spaced-secret-value  "})

	cases := []struct {
		name     string
		in       string
		contains []string
		excludes []string
		minCount int
	}{
		{
			name:     "known secret value",
			in:       "db error: auth failed for password hunter2secret retrying",
			excludes: []string{"hunter2secret"},
			minCount: 1,
		},
		{
			name:     "short values stay",
			in:       "level=short msg=ok",
			contains: []string{"short"},
		},
		{
			name:     "credential assignment",
			in:       "MYSQL_ROOT_PASSWORD=supersafe123\napi_key: abc123def\nDEBUG=true",
			contains: []string{"MYSQL_ROOT_PASSWORD=[REDACTED]", "api_key: [REDACTED]", "DEBUG=true"},
			excludes: []string{"supersafe123", "abc123def"},
			minCount: 2,
		},
		{
			name:     "trimmed secret",
			in:       "token is spaced-secret-value here",
			excludes: []string{"spaced-secret-value"},
			minCount: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, count := r.Redact(tc.in)
			for _, want := range tc.contains {
				if !strings.Contains(out, want) {
					t.Errorf("output %q missing %q", out, want)
				}
			}
			for _, banned := range tc.excludes {
				if strings.Contains(out, banned) {
					t.Errorf("output %q still contains %q", out, banned)
				}
			}
			if count < tc.minCount {
				t.Errorf("count = %d, want >= %d", count, tc.minCount)
			}
		})
	}
}

func TestOpenAICompatibleComplete(t *testing.T) {
	var gotAuth string
	var gotPayload map[string]interface{}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotPayload)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"model": "test-model",
			"choices": []map[string]interface{}{
				{"message": map[string]string{"role": "assistant", "content": "diagnosis here"}},
			},
			"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	defer fake.Close()

	p, err := New(&config.AIConfig{
		Enabled: true,
		BaseURL: fake.URL + "/v1/",
		APIKey:  "sk-test",
		Model:   "test-model",
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := p.Complete(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "diagnosis here" || resp.Model != "test-model" {
		t.Errorf("resp = %+v", resp)
	}
	if resp.Usage.PromptTokens != 10 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("auth header = %q", gotAuth)
	}
	if gotPayload["model"] != "test-model" {
		t.Errorf("payload model = %v", gotPayload["model"])
	}
}

func TestOpenAICompatibleKeyless(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("keyless request sent auth header %q", auth)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{{"message": map[string]string{"content": "ok"}}},
		})
	}))
	defer fake.Close()

	p, _ := New(&config.AIConfig{Enabled: true, BaseURL: fake.URL, Model: "llama3"})
	resp, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Model != "llama3" {
		t.Errorf("model fallback = %q, want configured model", resp.Model)
	}
}

func TestOpenAICompatibleErrorMapping(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer fake.Close()

	p, _ := New(&config.AIConfig{Enabled: true, BaseURL: fake.URL, Model: "m"})
	_, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "invalid api key") || !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %v, want provider message and status", err)
	}
}

func TestBuildDiagnosisMessagesTruncates(t *testing.T) {
	long := strings.Repeat("x", contextBudget*2)
	msgs := BuildDiagnosisMessages("myapp", "services: {}", "Recent logs", long)
	if len(msgs) != 2 {
		t.Fatalf("got %d messages", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[1].Role != "user" {
		t.Errorf("roles = %s/%s", msgs[0].Role, msgs[1].Role)
	}
	if len(msgs[1].Content) > contextBudget+2000 {
		t.Errorf("user message not truncated: %d chars", len(msgs[1].Content))
	}
	if !strings.Contains(msgs[1].Content, "[... truncated ...]") {
		t.Error("truncation marker missing")
	}
	if !strings.HasSuffix(msgs[1].Content, "x```") && !strings.Contains(msgs[1].Content, strings.Repeat("x", 100)+"\n```") {
		// The tail (newest content) must survive truncation.
		if !strings.Contains(msgs[1].Content, strings.Repeat("x", 100)) {
			t.Error("log tail missing from prompt")
		}
	}
}
