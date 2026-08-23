package cluster

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	c := NewClient("https://example.com", "test-key", 10*time.Second)

	if c.baseURL != "https://example.com" {
		t.Errorf("baseURL = %s, want https://example.com", c.baseURL)
	}
	if c.apiKey != "test-key" {
		t.Errorf("apiKey = %s, want test-key", c.apiKey)
	}
}

func TestClientSetsAuthHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer my-api-key" {
			t.Errorf("Authorization = %s, want Bearer my-api-key", auth)
		}
		ct := r.Header.Get("Content-Type")
		if ct != "application/json" {
			t.Errorf("Content-Type = %s, want application/json", ct)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx := context.Background()
	c := NewClient(server.URL, "my-api-key", 5*time.Second)
	resp, err := c.Do(ctx, "GET", "/test", nil)
	if err != nil {
		t.Fatalf("Do failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status = %d, want 200", resp.StatusCode)
	}
}

func TestClientHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			t.Errorf("Path = %s, want /api/health", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := NewClient(server.URL, "key", 5*time.Second)
	err := c.Health(context.Background())
	if err != nil {
		t.Errorf("Health check failed: %v", err)
	}
}

func TestClientHealthFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	c := NewClient(server.URL, "key", 5*time.Second)
	err := c.Health(context.Background())
	if err == nil {
		t.Error("Health should fail for 503 response")
	}
}

func TestClientHealthUnreachable(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "key", 1*time.Second)
	err := c.Health(context.Background())
	if err == nil {
		t.Error("Health should fail for unreachable server")
	}
}

func TestClientGet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("Method = %s, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	c := NewClient(server.URL, "key", 5*time.Second)
	data, status, err := c.Get(context.Background(), "/api/test")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("Status = %d, want 200", status)
	}

	var resp map[string]string
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("status = %s, want ok", resp["status"])
	}
}

func TestClientPost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Method = %s, want POST", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]string
		json.Unmarshal(body, &req)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"received": req["msg"]})
	}))
	defer server.Close()

	c := NewClient(server.URL, "key", 5*time.Second)
	body, _ := json.Marshal(map[string]string{"msg": "hello"})
	data, status, err := c.Post(context.Background(), "/api/test", body)
	if err != nil {
		t.Fatalf("Post failed: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("Status = %d, want 200", status)
	}

	var resp map[string]string
	json.Unmarshal(data, &resp)
	if resp["received"] != "hello" {
		t.Errorf("received = %s, want hello", resp["received"])
	}
}

func TestClientForward(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom-Header", "test-value")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"forwarded":true}`))
	}))
	defer server.Close()

	c := NewClient(server.URL, "key", 5*time.Second)
	data, status, headers, err := c.Forward(context.Background(), "PUT", "/api/resource", strings.NewReader(`{"data":"test"}`))
	if err != nil {
		t.Fatalf("Forward failed: %v", err)
	}
	if status != http.StatusCreated {
		t.Errorf("Status = %d, want 201", status)
	}
	if headers["X-Custom-Header"] != "test-value" {
		t.Errorf("X-Custom-Header = %s, want test-value", headers["X-Custom-Header"])
	}

	var resp map[string]bool
	json.Unmarshal(data, &resp)
	if !resp["forwarded"] {
		t.Error("Expected forwarded=true")
	}
}

func TestClientForwardsQueryAndRepresentationHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/api/deployments/app/files" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.URL.RawQuery != "path=%2Fconfig" {
			t.Fatalf("query = %q", r.URL.RawQuery)
		}
		if got := r.Header.Get("Content-Type"); got != "multipart/form-data; boundary=test" {
			t.Fatalf("content type = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/octet-stream" {
			t.Fatalf("accept = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer peer-key" {
			t.Fatalf("authorization = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(server.URL+"/agent", "peer-key", 5*time.Second)
	headers := http.Header{
		"Authorization": []string{"Bearer local-user-key"},
		"Content-Type":  []string{"multipart/form-data; boundary=test"},
		"Accept":        []string{"application/octet-stream"},
	}
	resp, err := client.DoWithHeaders(
		context.Background(),
		http.MethodPost,
		"/api/deployments/app/files?path=%2Fconfig",
		headers,
		strings.NewReader("payload"),
	)
	if err != nil {
		t.Fatalf("forward request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}
