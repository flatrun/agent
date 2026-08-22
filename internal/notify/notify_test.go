package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	shoutrrrsmtp "github.com/nicholas-fedor/shoutrrr/pkg/services/email/smtp"
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

func TestEmailMessageEmbedsImagesReferencedByContentID(t *testing.T) {
	png := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	message, err := buildEmailMessage(Notification{
		Title: "Alert", Message: "Details", Panels: []Panel{{Title: "CPU", ImageURL: png}},
	}, &shoutrrrsmtp.Config{
		FromAddress: "alerts@example.com", ToAddresses: []string{"admin@example.com"}, Subject: "Alert",
	})
	if err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	if _, err := message.WriteTo(&raw); err != nil {
		t.Fatal(err)
	}
	body := raw.String()
	for _, expected := range []string{
		"multipart/related", "cid:flatrun-logo.png", "cid:flatrun-panel-1.png",
		"Content-Id: <flatrun-logo.png>", "Content-Id: <flatrun-panel-1.png>",
		"Content-Disposition: inline",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("serialized email missing %q", expected)
		}
	}
	if strings.Contains(body, "data:image/png;base64,") {
		t.Error("serialized email still contains a data URL")
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
	if !strings.Contains(sent[0], "useHTML=yes") || !strings.Contains(sent[0], "subject=Alert") {
		t.Errorf("email delivery missing HTML or subject settings: %q", sent[0])
	}
	if !strings.Contains(sent[0], "<h1") || !strings.Contains(sent[0], "web is unhealthy") {
		t.Errorf("email delivery missing rendered template: %q", sent[0])
	}
	if want := "Alert\n\nweb is unhealthy"; !strings.Contains(sent[1], want) {
		t.Errorf("webhook delivery missing plain title and body: %q", sent[1])
	}
}

func TestEmailTemplateEscapesContentAndSetsSubject(t *testing.T) {
	target, body, err := formatDelivery(
		"smtp://mail.example/?from=ops%40example.com&to=admin%40example.com",
		Notification{Title: "Disk <critical>", Message: "Usage is {{high}} & rising"},
	)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Query().Get("subject"); got != "Disk <critical>" {
		t.Errorf("subject = %q", got)
	}
	if got := parsed.Query().Get("useHTML"); got != "yes" {
		t.Errorf("useHTML = %q", got)
	}
	if strings.Contains(body, "<critical>") || strings.Contains(body, "Usage is {{high}} & rising") {
		t.Errorf("email content was not escaped: %s", body)
	}
	if !strings.Contains(body, "Disk &lt;critical&gt;") || !strings.Contains(body, "Usage is {{high}} &amp; rising") {
		t.Errorf("email content is missing: %s", body)
	}
}

func TestNonEmailDeliveryRemainsPlainText(t *testing.T) {
	target, body, err := formatDelivery(
		"generic+https://example.com/hook",
		Notification{Title: "Alert", Message: "web is unhealthy"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if target != "generic+https://example.com/hook" || body != "Alert\n\nweb is unhealthy" {
		t.Errorf("plain delivery = %q | %q", target, body)
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
	if !strings.HasPrefix(called, "smtp://x?") || !strings.Contains(called, "subject=FlatRun+test+notification") {
		t.Errorf("Test sent to %q", called)
	}
	if err := s.Test(""); err == nil {
		t.Error("Test with empty url should error")
	}
}

func TestEmailSubjectCannotAddHeaders(t *testing.T) {
	target, _, err := formatDelivery(
		"smtp://mail.example",
		Notification{Title: "Alert\r\nBcc: other@example.com", Message: "message"},
	)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Query().Get("subject"); got != "Alert Bcc: other@example.com" {
		t.Errorf("subject = %q", got)
	}
}

func TestEmailVariantsAndPanels(t *testing.T) {
	png := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	cases := []struct {
		kind       Kind
		label      string
		color      string
		showStatus bool
	}{
		{KindGeneric, "", "", false},
		{KindPositive, "Resolved", "#15803d", true},
		{KindNegative, "Attention required", "#b91c1c", true},
	}
	for _, tc := range cases {
		body, err := RenderEmail(Notification{
			Kind: tc.kind, Title: "Status", Message: "Details",
			Panels: []Panel{{Title: "CPU", Value: "82%", Detail: "Last 15 minutes", ImageURL: png}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if tc.showStatus && (!strings.Contains(body, tc.label) || !strings.Contains(body, tc.color)) {
			t.Errorf("%s template missing status treatment", tc.kind)
		}
		if !tc.showStatus && strings.Contains(body, ">Notification</span>") {
			t.Errorf("%s template has redundant status treatment", tc.kind)
		}
		if !strings.Contains(body, "CPU") || !strings.Contains(body, "82%") {
			t.Errorf("%s template missing panel", tc.kind)
		}
		if !strings.Contains(body, "data:image/png;base64,") {
			t.Errorf("%s template missing PNG panel", tc.kind)
		}
		if !strings.Contains(body, "alt=\"FlatRun\"") || !strings.Contains(body, "data:image/png;base64,") {
			t.Errorf("%s template missing shared header", tc.kind)
		}
		if !strings.Contains(body, ">FlatRun</td>") {
			t.Errorf("%s template missing shared footer", tc.kind)
		}
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
