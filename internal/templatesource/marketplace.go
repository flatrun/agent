package templatesource

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// MarketplaceSource delivers the catalog from the FlatRun marketplace agent-sync
// API (GET {base}/agent/templates). The marketplace stores compose and metadata
// inline, so a template arrives fully formed. Entries whose compose is empty are
// backed by an external git repo on the marketplace side and are skipped here;
// resolving those is a follow-up. This source is gated off by default until the
// marketplace API is declared ready; flipping the config flag makes it
// authoritative ahead of GitHub with no code change.
type MarketplaceSource struct {
	// BaseURL is the marketplace API root, e.g. "https://api.flatrun.dev/api/v1".
	BaseURL string
	// Enabled gates the source from config.
	Enabled bool
	// Client is the HTTP client; a default with a timeout is used when nil.
	Client *http.Client
}

func (m MarketplaceSource) Name() string { return "marketplace" }

func (m MarketplaceSource) Available(ctx context.Context) bool {
	return m.Enabled && m.BaseURL != ""
}

func (m MarketplaceSource) client() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// agentTemplate mirrors the marketplace AgentTemplateResource. mounts and files
// are passed through untouched into the reconstructed metadata.yml, so this
// source needs no knowledge of their inner shape.
type agentTemplate struct {
	ID            string      `json:"id" yaml:"-"`
	Name          string      `json:"name" yaml:"name"`
	Description   string      `json:"description" yaml:"description,omitempty"`
	Icon          string      `json:"icon" yaml:"icon,omitempty"`
	Logo          string      `json:"logo" yaml:"logo,omitempty"`
	Category      string      `json:"category" yaml:"category,omitempty"`
	Priority      int         `json:"priority" yaml:"priority,omitempty"`
	ContainerPort int         `json:"container_port" yaml:"container_port,omitempty"`
	Mounts        interface{} `json:"mounts" yaml:"mounts,omitempty"`
	Files         interface{} `json:"files" yaml:"files,omitempty"`
	Content       string      `json:"content" yaml:"-"`
	Version       string      `json:"version" yaml:"-"`
}

func (m MarketplaceSource) List(ctx context.Context) ([]Template, error) {
	url := strings.TrimRight(m.BaseURL, "/") + "/agent/templates"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := m.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent/templates: status %d", resp.StatusCode)
	}

	// Laravel resource collections wrap the array in "data".
	var payload struct {
		Data []agentTemplate `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	out := make([]Template, 0, len(payload.Data))
	for _, at := range payload.Data {
		if at.ID == "" || at.Content == "" {
			// Empty content means the template is backed by an external repo on
			// the marketplace; skip until repo-backed entries are supported.
			continue
		}
		metadata, err := yaml.Marshal(at)
		if err != nil {
			continue
		}
		out = append(out, Template{
			ID:       at.ID,
			Version:  at.Version,
			Metadata: metadata,
			Compose:  []byte(at.Content),
		})
	}
	return out, nil
}
