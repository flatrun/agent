package notify

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html/template"
	"image/color"
	"math"
	"net/url"
	"strings"
)

type Kind string

const (
	KindGeneric  Kind = "generic"
	KindPositive Kind = "positive"
	KindNegative Kind = "negative"
	KindWarning  Kind = "warning"
)

type Panel struct {
	Title    string     `json:"title,omitempty"`
	Value    string     `json:"value,omitempty"`
	Detail   string     `json:"detail,omitempty"`
	ImageURL string     `json:"image_url,omitempty"`
	Chart    *Chart     `json:"chart,omitempty"`
	Rows     []PanelRow `json:"rows,omitempty"`
}

type PanelRow struct {
	Label string  `json:"label"`
	Value string  `json:"value"`
	Share float64 `json:"share,omitempty"`
}

type Action struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type Notification struct {
	Kind    Kind    `json:"type,omitempty"`
	Title   string  `json:"title"`
	Message string  `json:"message"`
	Action  *Action `json:"action,omitempty"`
	Panels  []Panel `json:"panels,omitempty"`
}

type emailPanel struct {
	Title    string
	Value    string
	Detail   string
	ImageURL template.URL
	Rows     []emailRow
}

type emailRow struct {
	Label   string
	Value   string
	Percent int
}

type emailData struct {
	Title       string
	Message     string
	Logo        template.URL
	Accent      string
	AccentSoft  string
	Status      string
	ActionLabel string
	ActionURL   template.URL
	Panels      []emailPanel
	// Outlook ignores max-width, so it gets a fixed-width table that other
	// clients skip. html/template drops comments written in the template itself.
	OutlookOpen  template.HTML
	OutlookClose template.HTML
}

type emailEmbed struct {
	name string
	mime string
	data []byte
}

type renderedEmail struct {
	html   string
	embeds []emailEmbed
}

func normalizeKind(kind Kind) Kind {
	switch kind {
	case KindPositive, KindNegative, KindWarning:
		return kind
	default:
		return KindGeneric
	}
}

func accentRGBA(kind Kind) color.RGBA {
	var c color.RGBA
	accent, _, _ := palette(kind)
	_, _ = fmt.Sscanf(accent, "#%02x%02x%02x", &c.R, &c.G, &c.B)
	c.A = 255
	return c
}

// withCharts draws each panel's chart into its image, so every renderer and the
// attachment step see an ordinary PNG panel.
func withCharts(notification Notification) Notification {
	panels := make([]Panel, len(notification.Panels))
	for i, panel := range notification.Panels {
		if panel.Chart != nil && panel.ImageURL == "" {
			if image, ok := chartPNG(*panel.Chart, accentRGBA(notification.Kind)); ok {
				panel.ImageURL = image
			}
		}
		panels[i] = panel
	}
	notification.Panels = panels
	return notification
}

func safeActionURL(raw string) template.URL {
	parsed, err := url.Parse(raw)
	if err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" {
		return template.URL(raw)
	}
	return ""
}

func palette(kind Kind) (string, string, string) {
	switch normalizeKind(kind) {
	case KindPositive:
		return "#15803d", "#dcfce7", "Resolved"
	case KindNegative:
		return "#b91c1c", "#fee2e2", "Attention required"
	case KindWarning:
		return "#ca8a04", "#fef9c3", "Warning"
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
	logo := "data:" + defaultEmailTheme.logoMIME + ";base64," + base64.StdEncoding.EncodeToString(defaultEmailTheme.logo)
	return renderEmail(withCharts(notification), template.URL(logo), false)
}

func renderEmailForDelivery(notification Notification) (renderedEmail, error) {
	notification = withCharts(notification)
	const logoName = "flatrun-logo.png"
	body, err := renderEmail(notification, template.URL("cid:"+logoName), true)
	if err != nil {
		return renderedEmail{}, err
	}
	embeds := []emailEmbed{{name: logoName, mime: defaultEmailTheme.logoMIME, data: defaultEmailTheme.logo}}
	for i, panel := range notification.Panels {
		data, ok := pngData(panel.ImageURL)
		if !ok {
			continue
		}
		embeds = append(embeds, emailEmbed{
			name: fmt.Sprintf("flatrun-panel-%d.png", i+1), mime: "image/png", data: data,
		})
	}
	return renderedEmail{html: body, embeds: embeds}, nil
}

func renderEmail(notification Notification, logo template.URL, embedPanels bool) (string, error) {
	accent, accentSoft, status := palette(notification.Kind)
	panels := make([]emailPanel, 0, len(notification.Panels))
	for i, panel := range notification.Panels {
		imageURL := safeImageURL(panel.ImageURL)
		if embedPanels {
			if _, ok := pngData(panel.ImageURL); ok {
				imageURL = template.URL(fmt.Sprintf("cid:flatrun-panel-%d.png", i+1))
			}
		}
		rows := make([]emailRow, 0, len(panel.Rows))
		for _, row := range panel.Rows {
			percent := int(math.Round(math.Max(0, math.Min(1, row.Share)) * 100))
			rows = append(rows, emailRow{Label: row.Label, Value: row.Value, Percent: percent})
		}
		panels = append(panels, emailPanel{
			Title: panel.Title, Value: panel.Value, Detail: panel.Detail, ImageURL: imageURL, Rows: rows,
		})
	}
	var actionLabel string
	var actionURL template.URL
	if notification.Action != nil {
		actionURL = safeActionURL(notification.Action.URL)
		actionLabel = notification.Action.Label
		if actionLabel == "" {
			actionLabel = "Open"
		}
	}
	var body bytes.Buffer
	err := defaultEmailTheme.template.ExecuteTemplate(&body, defaultEmailTheme.main, emailData{
		Title: notification.Title, Message: notification.Message, Logo: logo,
		Accent: accent, AccentSoft: accentSoft, Status: status, Panels: panels,
		ActionLabel: actionLabel, ActionURL: actionURL,
		OutlookOpen:  `<!--[if mso]><table role="presentation" width="600" cellspacing="0" cellpadding="0"><tr><td><![endif]-->`,
		OutlookClose: `<!--[if mso]></td></tr></table><![endif]-->`,
	})
	if err != nil {
		return "", fmt.Errorf("render email notification: %w", err)
	}
	return body.String(), nil
}

func pngData(raw string) ([]byte, bool) {
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(raw, prefix) {
		return nil, false
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(raw, prefix))
	return data, err == nil && len(data) >= 8 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n"))
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
