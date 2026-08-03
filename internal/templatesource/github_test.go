package templatesource

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// tarball builds a gzipped tar shaped like a GitHub codeload archive: every
// entry is wrapped in a top-level "<name>/" directory.
func tarball(t *testing.T, top string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		full := top + "/" + name
		if err := tw.WriteHeader(&tar.Header{Name: full, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestGitHubSourceListExtractsTemplates(t *testing.T) {
	archive := tarball(t, "templates-main", map[string]string{
		"README.md":                    "# templates",
		"wordpress/metadata.yml":       "name: WordPress\npriority: 100\n",
		"wordpress/docker-compose.yml": "name: ${NAME}\nservices:\n  wordpress:\n",
		"static/docker-compose.yml":    "name: ${NAME}\nservices:\n  web:\n",
		"static/html/index.html":       "<h1>${NAME}</h1>",
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	src := GitHubSource{Repo: "flatrun/templates", Ref: "main", Enabled: true, BaseURL: srv.URL}
	got, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	byID := map[string]Template{}
	for _, tpl := range got {
		byID[tpl.ID] = tpl
	}

	if len(byID) != 2 {
		t.Fatalf("got %d templates, want 2 (wordpress, static); README must be ignored", len(byID))
	}

	wp, ok := byID["wordpress"]
	if !ok {
		t.Fatal("missing wordpress template")
	}
	if !bytes.Contains(wp.Metadata, []byte("WordPress")) {
		t.Errorf("wordpress metadata not captured: %q", wp.Metadata)
	}
	if !bytes.Contains(wp.Compose, []byte("wordpress:")) {
		t.Errorf("wordpress compose not captured: %q", wp.Compose)
	}

	static, ok := byID["static"]
	if !ok {
		t.Fatal("missing static template")
	}
	if len(static.Metadata) != 0 {
		t.Errorf("static should have no metadata, got %q", static.Metadata)
	}
	if got := static.Files["html/index.html"]; string(got) != "<h1>${NAME}</h1>" {
		t.Errorf("static nested file not captured under relative path, got %q", got)
	}
}

func TestGitHubSourceListErrorsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	src := GitHubSource{Repo: "flatrun/templates", Enabled: true, BaseURL: srv.URL}
	if _, err := src.List(context.Background()); err == nil {
		t.Fatal("expected error on 404")
	}
}

func TestGitHubSourceAvailable(t *testing.T) {
	if (GitHubSource{Enabled: false, Repo: "flatrun/templates"}).Available(context.Background()) {
		t.Error("disabled source must not be available")
	}
	if (GitHubSource{Enabled: true, Repo: ""}).Available(context.Background()) {
		t.Error("source without a repo must not be available")
	}
	if !(GitHubSource{Enabled: true, Repo: "flatrun/templates"}).Available(context.Background()) {
		t.Error("configured source should be available")
	}
}
