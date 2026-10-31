package templatesource

import (
	"context"
	"encoding/json"
	"github.com/flatrun/agent/pkg/securityprofile"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMarketplaceSecurityProfileCapturedHTTPRoundTrip(t *testing.T) {
	payload, err := os.ReadFile("testdata/marketplace-security-profile.json")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	source := MarketplaceSource{BaseURL: server.URL, Enabled: true}
	templates, err := source.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) != 1 {
		t.Fatalf("captured templates: %d", len(templates))
	}
	var metadata struct {
		Profile securityprofile.Profile `yaml:"security_profile"`
	}
	if err = yaml.Unmarshal(templates[0].Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if err = metadata.Profile.Validate(); err != nil {
		t.Fatal("profile became unsupported during transport", err)
	}
	var captured struct {
		Data []struct {
			Profile any `json:"security_profile"`
		} `json:"data"`
	}
	if err = json.Unmarshal(payload, &captured); err != nil {
		t.Fatal(err)
	}
	if securityprofile.Digest(metadata.Profile) != securityprofile.Digest(captured.Data[0].Profile) {
		actual, _ := json.Marshal(metadata.Profile)
		expected, _ := json.Marshal(captured.Data[0].Profile)
		t.Fatalf("profile semantics changed during metadata reconstruction\nactual: %s\nexpected: %s", actual, expected)
	}
}

// realAgentTemplatesResponse is the AgentTemplateResource collection shape from
// marketplace-webservice (app/Http/Resources/Api/V1/Agent/AgentTemplateResource.php):
// a Laravel resource collection wrapped in "data", each item carrying inline
// compose in "content". The github/gitlab-backed entry has empty content.
const realAgentTemplatesResponse = `{
  "data": [
    {
      "id": "wordpress",
      "name": "WordPress",
      "description": "WordPress with MySQL",
      "icon": "pi pi-wordpress",
      "logo": "https://cdn.flatrun.dev/wordpress.svg",
      "category": "application",
      "priority": 100,
      "container_port": 80,
      "mounts": [
        {"id": "content", "name": "Content", "container_path": "/var/www/html/wp-content", "type": "directory"}
      ],
      "files": [],
      "content": "name: ${NAME}\nservices:\n  wordpress:\n    image: wordpress:latest\n"
    },
    {
      "id": "external-app",
      "name": "External App",
      "description": "Backed by a git repo",
      "icon": "pi pi-box",
      "logo": "",
      "category": "application",
      "priority": 10,
      "container_port": 3000,
      "mounts": [],
      "files": [],
      "content": ""
    }
  ]
}`

func TestMarketplaceSourceListParsesRealShape(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(realAgentTemplatesResponse))
	}))
	defer srv.Close()

	src := MarketplaceSource{BaseURL: srv.URL + "/api/v1", Enabled: true}
	got, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if gotPath != "/api/v1/agent/templates" {
		t.Errorf("requested %q, want /api/v1/agent/templates", gotPath)
	}

	// The empty-content (repo-backed) entry is skipped.
	if len(got) != 1 {
		t.Fatalf("got %d templates, want 1 (repo-backed entry skipped)", len(got))
	}

	wp := got[0]
	if wp.ID != "wordpress" {
		t.Fatalf("id = %q, want wordpress", wp.ID)
	}
	if !strings.Contains(string(wp.Compose), "image: wordpress:latest") {
		t.Errorf("compose not taken from content: %q", wp.Compose)
	}

	// Metadata is reconstructed as YAML the on-disk read path can parse back.
	var meta struct {
		Name          string        `yaml:"name"`
		Category      string        `yaml:"category"`
		Priority      int           `yaml:"priority"`
		ContainerPort int           `yaml:"container_port"`
		Mounts        []interface{} `yaml:"mounts"`
	}
	if err := yaml.Unmarshal(wp.Metadata, &meta); err != nil {
		t.Fatalf("reconstructed metadata is not valid yaml: %v\n%s", err, wp.Metadata)
	}
	if meta.Name != "WordPress" || meta.Priority != 100 || meta.ContainerPort != 80 {
		t.Errorf("metadata fields lost: %+v", meta)
	}
	if len(meta.Mounts) != 1 {
		t.Errorf("mounts passthrough lost: %+v", meta.Mounts)
	}
}

func TestMarketplaceSourceAvailable(t *testing.T) {
	if (MarketplaceSource{Enabled: false, BaseURL: "http://x"}).Available(context.Background()) {
		t.Error("disabled marketplace must not be available")
	}
	if (MarketplaceSource{Enabled: true, BaseURL: ""}).Available(context.Background()) {
		t.Error("marketplace without a url must not be available")
	}
}
