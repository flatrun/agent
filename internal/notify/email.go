package notify

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/url"
	"strings"
)

type Kind string

const (
	KindGeneric  Kind = "generic"
	KindPositive Kind = "positive"
	KindNegative Kind = "negative"
)

type Panel struct {
	Title    string `json:"title,omitempty"`
	Value    string `json:"value,omitempty"`
	Detail   string `json:"detail,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type Notification struct {
	Kind    Kind    `json:"type,omitempty"`
	Title   string  `json:"title"`
	Message string  `json:"message"`
	Panels  []Panel `json:"panels,omitempty"`
}

type emailPanel struct {
	Title    string
	Value    string
	Detail   string
	ImageURL template.URL
}

type emailData struct {
	Title      string
	Message    string
	Logo       template.URL
	Accent     string
	AccentSoft string
	Status     string
	Panels     []emailPanel
}

func normalizeKind(kind Kind) Kind {
	switch kind {
	case KindPositive, KindNegative:
		return kind
	default:
		return KindGeneric
	}
}

func palette(kind Kind) (string, string, string) {
	switch normalizeKind(kind) {
	case KindPositive:
		return "#15803d", "#dcfce7", "Resolved"
	case KindNegative:
		return "#b91c1c", "#fee2e2", "Attention required"
	default:
		return "#1d4ed8", "#dbeafe", ""
	}
}

func safeImageURL(raw string) template.URL {
	parsed, err := url.Parse(raw)
	if err == nil && parsed.Scheme == "https" && parsed.Host != "" {
		return template.URL(raw)
	}
	const prefix = "data:image/png;base64,"
	if strings.HasPrefix(raw, prefix) {
		data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(raw, prefix))
		if err == nil && len(data) >= 8 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")) {
			return template.URL(raw)
		}
	}
	return ""
}

func RenderEmail(notification Notification) (string, error) {
	accent, accentSoft, status := palette(notification.Kind)
	panels := make([]emailPanel, 0, len(notification.Panels))
	for _, panel := range notification.Panels {
		panels = append(panels, emailPanel{
			Title: panel.Title, Value: panel.Value, Detail: panel.Detail, ImageURL: safeImageURL(panel.ImageURL),
		})
	}
	var body bytes.Buffer
	err := defaultEmailTheme.template.ExecuteTemplate(&body, defaultEmailTheme.main, emailData{
		Title: notification.Title, Message: notification.Message, Logo: defaultEmailTheme.logo,
		Accent: accent, AccentSoft: accentSoft, Status: status, Panels: panels,
	})
	if err != nil {
		return "", fmt.Errorf("render email notification: %w", err)
	}
	return body.String(), nil
}

func formatDelivery(rawURL string, notification Notification) (string, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("parse notification target: %w", err)
	}
	if parsed.Scheme != "smtp" {
		if notification.Title == "" {
			return rawURL, notification.Message, nil
		}
		return rawURL, notification.Title + "\n\n" + notification.Message, nil
	}
	query := parsed.Query()
	query.Set("useHTML", "yes")
	if notification.Title != "" {
		query.Set("subject", strings.Join(strings.Fields(notification.Title), " "))
	}
	parsed.RawQuery = query.Encode()
	body, err := RenderEmail(notification)
	return parsed.String(), body, err
}
