package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	luaDeploymentsPath = "/tmp/flatrun-e2e-lua"
	luaAPIPort         = "18092"
	luaNginxPort       = "18082"
)

func TestLuaRealtimeCapture(t *testing.T) {
	if os.Getenv("FLATRUN_LUA_TEST") != "true" {
		t.Skip("Skipping Lua test - set FLATRUN_LUA_TEST=true to run")
	}

	startComposeEnv(t, "docker-compose.lua.yml", luaDeploymentsPath, 120*time.Second)

	// Extra settle time for OpenResty
	time.Sleep(2 * time.Second)

	cleanupSecurityState(t)

	t.Run("security config files created", func(t *testing.T) {
		rateLimitsPath := filepath.Join(luaDeploymentsPath, "nginx", "conf.d", "rate_limits.conf")
		if _, err := os.Stat(rateLimitsPath); os.IsNotExist(err) {
			t.Fatalf("rate_limits.conf should exist")
		}
	})

	t.Run("openresty is healthy", func(t *testing.T) {
		resp, err := http.Get(fmt.Sprintf("http://localhost:%s/health", luaNginxPort))
		if err != nil {
			t.Fatalf("OpenResty health check failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("OpenResty health check returned %d, expected 200", resp.StatusCode)
		}
	})

	t.Run("403 triggers security event", func(t *testing.T) {
		initialCount := getEventCount(t)

		req, _ := http.NewRequest("GET", fmt.Sprintf("http://localhost:%s/admin", luaNginxPort), nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 FlatRunTest")

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Request to /admin failed: %v", err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("Expected 403, got %d", resp.StatusCode)
		}

		time.Sleep(2 * time.Second)

		newCount := getEventCount(t)
		if newCount <= initialCount {
			t.Errorf("Expected event count to increase, was %d, now %d", initialCount, newCount)
		}
		t.Logf("Event count increased from %d to %d", initialCount, newCount)
	})

	t.Run("401 triggers security event", func(t *testing.T) {
		initialCount := getEventCount(t)

		req, _ := http.NewRequest("GET", fmt.Sprintf("http://localhost:%s/api/private", luaNginxPort), nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 FlatRunTest")

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Request to /api/private failed: %v", err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Expected 401, got %d", resp.StatusCode)
		}

		time.Sleep(2 * time.Second)

		newCount := getEventCount(t)
		if newCount <= initialCount {
			t.Errorf("Expected event count to increase, was %d, now %d", initialCount, newCount)
		}
		t.Logf("Event count increased from %d to %d", initialCount, newCount)
	})

	t.Run("500 triggers security event", func(t *testing.T) {
		initialCount := getEventCount(t)

		req, _ := http.NewRequest("GET", fmt.Sprintf("http://localhost:%s/error", luaNginxPort), nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 FlatRunTest")

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Request to /error failed: %v", err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("Expected 500, got %d", resp.StatusCode)
		}

		time.Sleep(2 * time.Second)

		newCount := getEventCount(t)
		if newCount <= initialCount {
			t.Errorf("Expected event count to increase, was %d, now %d", initialCount, newCount)
		}
		t.Logf("Event count increased from %d to %d", initialCount, newCount)
	})

	t.Run("suspicious path triggers security event", func(t *testing.T) {
		initialCount := getEventCount(t)

		req, _ := http.NewRequest("GET", fmt.Sprintf("http://localhost:%s/.env", luaNginxPort), nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 FlatRunTest")

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Request to /.env failed: %v", err)
		}
		resp.Body.Close()

		time.Sleep(2 * time.Second)

		newCount := getEventCount(t)
		if newCount <= initialCount {
			t.Errorf("Expected event count to increase for suspicious path, was %d, now %d", initialCount, newCount)
		}
		t.Logf("Event count increased from %d to %d", initialCount, newCount)
	})

	t.Run("scanner user agent triggers security event", func(t *testing.T) {
		initialCount := getEventCount(t)

		req, _ := http.NewRequest("GET", fmt.Sprintf("http://localhost:%s/", luaNginxPort), nil)
		req.Header.Set("User-Agent", "nikto/2.1.6")

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Request with scanner UA failed: %v", err)
		}
		resp.Body.Close()

		time.Sleep(2 * time.Second)

		newCount := getEventCount(t)
		if newCount <= initialCount {
			t.Errorf("Expected event count to increase for scanner UA, was %d, now %d", initialCount, newCount)
		}
		t.Logf("Event count increased from %d to %d", initialCount, newCount)
	})

	t.Run("events have correct data", func(t *testing.T) {
		events := getEvents(t)
		if len(events) == 0 {
			t.Fatal("Expected at least one event")
		}

		for _, event := range events {
			if event.SourceIP == "" {
				t.Error("Event missing source_ip")
			}
			if event.RequestPath == "" {
				t.Error("Event missing request_path")
			}
			if event.StatusCode == 0 {
				t.Error("Event missing status_code")
			}
			if event.EventType == "" {
				t.Error("Event missing event_type")
			}
			if event.Severity == "" {
				t.Error("Event missing severity")
			}
		}
		t.Logf("Verified %d events have correct data", len(events))
	})

	t.Run("stats endpoint reflects captured events", func(t *testing.T) {
		resp, err := http.Get(fmt.Sprintf("http://localhost:%s/api/security/stats", luaAPIPort))
		if err != nil {
			t.Fatalf("Stats request failed: %v", err)
		}
		defer resp.Body.Close()

		var result struct {
			Stats struct {
				TotalEvents int            `json:"total_events"`
				BySeverity  map[string]int `json:"by_severity"`
				ByType      map[string]int `json:"by_type"`
			} `json:"stats"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("Failed to decode stats: %v", err)
		}

		if result.Stats.TotalEvents == 0 {
			t.Error("Expected total_events > 0")
		}
		t.Logf("Stats: total=%d, by_severity=%v, by_type=%v",
			result.Stats.TotalEvents, result.Stats.BySeverity, result.Stats.ByType)
	})
}

type SecurityEvent struct {
	ID          int    `json:"id"`
	SourceIP    string `json:"source_ip"`
	RequestPath string `json:"request_path"`
	StatusCode  int    `json:"status_code"`
	EventType   string `json:"event_type"`
	Severity    string `json:"severity"`
}

func getEventCount(t *testing.T) int {
	resp, err := http.Get(fmt.Sprintf("http://localhost:%s/api/security/stats", luaAPIPort))
	if err != nil {
		t.Fatalf("Failed to get stats: %v", err)
	}
	defer resp.Body.Close()

	var result struct {
		Stats struct {
			TotalEvents int `json:"total_events"`
		} `json:"stats"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode stats: %v", err)
	}

	return result.Stats.TotalEvents
}

func getEvents(t *testing.T) []SecurityEvent {
	resp, err := http.Get(fmt.Sprintf("http://localhost:%s/api/security/events?limit=100", luaAPIPort))
	if err != nil {
		t.Fatalf("Failed to get events: %v", err)
	}
	defer resp.Body.Close()

	var result struct {
		Events []SecurityEvent `json:"events"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode events: %v", err)
	}

	return result.Events
}

func cleanupSecurityState(t *testing.T) {
	resp, err := http.Get(fmt.Sprintf("http://localhost:%s/api/security/blocked-ips", luaAPIPort))
	if err != nil {
		t.Logf("Warning: Could not get blocked IPs: %v", err)
		return
	}
	defer resp.Body.Close()

	var result struct {
		BlockedIPs []struct {
			IP string `json:"ip"`
		} `json:"blocked_ips"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Logf("Warning: Could not decode blocked IPs: %v", err)
		return
	}

	client := &http.Client{}
	for _, blocked := range result.BlockedIPs {
		req, _ := http.NewRequest("DELETE", fmt.Sprintf("http://localhost:%s/api/security/blocked-ips/%s", luaAPIPort, blocked.IP), nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Logf("Warning: Could not unblock IP %s: %v", blocked.IP, err)
			continue
		}
		resp.Body.Close()
	}

	t.Logf("Cleaned up %d blocked IPs", len(result.BlockedIPs))
}
