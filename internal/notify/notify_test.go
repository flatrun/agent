package notify

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestTargetJSONMasksURL(t *testing.T) {
	b, err := json.Marshal(Target{ID: "1", Name: "email", URL: "smtp://user:secret@host:587", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	if strings.Contains(out, "secret") {
		t.Errorf("credential leaked in JSON: %s", out)
	}
	if !strings.Contains(out, MaskedURL) {
		t.Errorf("URL should be masked: %s", out)
	}
}

func TestUpdatePreservesMaskedURL(t *testing.T) {
	s := NewService(t.TempDir())
	const real = "smtp://user:secret@host:587"
	if err := s.Save(Config{Targets: []Target{{ID: "1", Name: "email", URL: real, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}

	// A client that received a masked URL saves it back with only a name change.
	if err := s.Update(Config{Targets: []Target{{ID: "1", Name: "renamed", URL: MaskedURL, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}

	got := s.Load()
	if len(got.Targets) != 1 || got.Targets[0].URL != real {
		t.Errorf("masked update must keep the stored URL, got %+v", got.Targets)
	}
	if got.Targets[0].Name != "renamed" {
		t.Errorf("non-secret changes should still apply, got name %q", got.Targets[0].Name)
	}
}

func TestServiceRoundTripAndNotify(t *testing.T) {
	s := NewService(t.TempDir())

	var sent []string
	s.send = func(url, msg string) error { sent = append(sent, url+"|"+msg); return nil }

	cfg := Config{Targets: []Target{
		{ID: "1", Name: "ops email", URL: "smtp://x", Enabled: true},
		{ID: "2", Name: "disabled", URL: "smtp://y", Enabled: false},
		{ID: "3", Name: "webhook", URL: "generic+https://h", Enabled: true},
	}}
	if err := s.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if got := s.Load(); len(got.Targets) != 3 {
		t.Fatalf("expected 3 targets persisted, got %d", len(got.Targets))
	}

	if err := s.Notify("Alert", "web is unhealthy"); err != nil {
		t.Fatalf("Notify = %v", err)
	}
	// Only the two enabled targets receive the message.
	if len(sent) != 2 {
		t.Fatalf("expected 2 deliveries (enabled only), got %d: %v", len(sent), sent)
	}
	for _, s := range sent {
		if want := "Alert\n\nweb is unhealthy"; !contains(s, want) {
			t.Errorf("delivery missing title+body: %q", s)
		}
	}
}

func TestNotifyTargetsDeliversToSubset(t *testing.T) {
	s := NewService(t.TempDir())
	var sent []string
	s.send = func(url, msg string) error { sent = append(sent, url); return nil }

	if err := s.Save(Config{Targets: []Target{
		{ID: "1", Name: "email", URL: "smtp://x", Enabled: true},
		{ID: "2", Name: "webhook", URL: "generic+https://h", Enabled: true},
	}}); err != nil {
		t.Fatal(err)
	}

	if err := s.NotifyTargets("Alert", "msg", []string{"2"}); err != nil {
		t.Fatalf("NotifyTargets = %v", err)
	}
	if len(sent) != 1 || sent[0] != "generic+https://h" {
		t.Fatalf("expected delivery only to target 2, got %v", sent)
	}
}

func TestServiceTest(t *testing.T) {
	s := NewService(t.TempDir())
	called := ""
	s.send = func(url, msg string) error { called = url; return nil }
	if err := s.Test("smtp://x"); err != nil {
		t.Fatal(err)
	}
	if called != "smtp://x" {
		t.Errorf("Test sent to %q", called)
	}
	if err := s.Test(""); err == nil {
		t.Error("Test with empty url should error")
	}
}

func TestNotifyReturnsFirstError(t *testing.T) {
	s := NewService(t.TempDir())
	_ = s.Save(Config{Targets: []Target{
		{ID: "1", URL: "a", Enabled: true},
		{ID: "2", URL: "b", Enabled: true},
	}})
	s.send = func(url, _ string) error {
		if url == "a" {
			return errors.New("boom")
		}
		return nil
	}
	if err := s.Notify("t", "m"); err == nil {
		t.Error("expected first delivery error surfaced")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
