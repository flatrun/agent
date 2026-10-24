package templatesource

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSyncerWritesCatalogToCache(t *testing.T) {
	cache := t.TempDir()
	src := stubSource{name: "github", available: true, templates: []Template{
		{
			ID:       "wordpress",
			Metadata: []byte("name: WordPress\n"),
			Compose:  []byte("services:\n  wordpress:\n"),
			Files:    map[string][]byte{"html/index.html": []byte("<h1>hi</h1>")},
		},
	}}
	s := &Syncer{Resolver: NewResolver(src), CacheDir: cache}

	name, n, err := s.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if name != "github" || n != 1 {
		t.Fatalf("got (%q, %d), want (github, 1)", name, n)
	}

	for rel, want := range map[string]string{
		"wordpress/metadata.yml":       "name: WordPress\n",
		"wordpress/docker-compose.yml": "services:\n  wordpress:\n",
		"wordpress/html/index.html":    "<h1>hi</h1>",
	} {
		got, err := os.ReadFile(filepath.Join(cache, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
}

func TestSyncerLeavesCacheOnNoSource(t *testing.T) {
	cache := t.TempDir()
	existing := filepath.Join(cache, "wordpress")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	compose := filepath.Join(existing, "docker-compose.yml")
	if err := os.WriteFile(compose, []byte("cached"), 0o644); err != nil {
		t.Fatal(err)
	}

	// No available source: the cache must be untouched.
	s := &Syncer{Resolver: NewResolver(stubSource{name: "github", available: false}), CacheDir: cache}
	name, n, err := s.Sync(context.Background())
	if err != nil || name != "" || n != 0 {
		t.Fatalf("got (%q, %d, %v), want (\"\", 0, nil)", name, n, err)
	}

	got, err := os.ReadFile(compose)
	if err != nil || string(got) != "cached" {
		t.Fatalf("cached template was disturbed: %q %v", got, err)
	}
}

func TestSyncerRejectsReservedIDs(t *testing.T) {
	cache := t.TempDir()
	// An embedded-style infra template the sync must never clobber.
	infra := filepath.Join(cache, "infra", "nginx")
	if err := os.MkdirAll(infra, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(infra, "docker-compose.yml"), []byte("embedded"), 0o644); err != nil {
		t.Fatal(err)
	}

	src := stubSource{name: "github", available: true, templates: []Template{
		{ID: "infra/nginx", Compose: []byte("hijacked")},
		{ID: "welcome", Compose: []byte("hijacked")},
		{ID: "wordpress", Compose: []byte("ok")},
	}}
	s := &Syncer{Resolver: NewResolver(src), CacheDir: cache}

	_, n, err := s.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if n != 1 {
		t.Fatalf("wrote %d templates, want 1 (reserved ids skipped)", n)
	}
	got, err := os.ReadFile(filepath.Join(infra, "docker-compose.yml"))
	if err != nil || string(got) != "embedded" {
		t.Fatalf("reserved infra template was overwritten: %q %v", got, err)
	}
}

func TestSyncerReplacesStaleFiles(t *testing.T) {
	cache := t.TempDir()
	dir := filepath.Join(cache, "wordpress")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stale.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	src := stubSource{name: "github", available: true, templates: []Template{
		{ID: "wordpress", Compose: []byte("services:\n"), Files: map[string][]byte{"fresh.txt": []byte("new")}},
	}}
	s := &Syncer{Resolver: NewResolver(src), CacheDir: cache}
	if _, _, err := s.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "stale.txt")); !os.IsNotExist(err) {
		t.Error("a file removed upstream should not linger in the cache")
	}
	if _, err := os.Stat(filepath.Join(dir, "fresh.txt")); err != nil {
		t.Errorf("the new file should be written: %v", err)
	}
}

func TestSyncerRejectsPathTraversal(t *testing.T) {
	cache := t.TempDir()
	src := stubSource{name: "github", available: true, templates: []Template{
		{ID: "../escape", Compose: []byte("x")},
	}}
	s := &Syncer{Resolver: NewResolver(src), CacheDir: cache}

	if _, _, err := s.Sync(context.Background()); err != nil {
		t.Fatalf("Sync should skip the bad template, not error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(cache), "escape")); err == nil {
		t.Fatal("path traversal escaped the cache dir")
	}
}

// codeloadStub mimics codeload.github.com: it serves the archive with an ETag
// and answers a matching If-None-Match with 304 and no body.
type codeloadStub struct {
	archive   []byte
	etag      string
	downloads int
}

func (c *codeloadStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("If-None-Match") == c.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	c.downloads++
	w.Header().Set("ETag", c.etag)
	w.Header().Set("Content-Type", "application/x-gzip")
	_, _ = w.Write(c.archive)
}

func TestSyncerDownloadsOnlyWhenTheCatalogChanged(t *testing.T) {
	stub := &codeloadStub{
		archive: tarball(t, "templates-main", map[string]string{"wordpress/docker-compose.yml": "services:\n  wordpress:\n"}),
		etag:    `"300144782d0e67acd1add2585914d8d53ab10a74ddf3345cf32869ecd5258b7a"`,
	}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	cache := t.TempDir()
	s := &Syncer{Resolver: NewResolver(GitHubSource{Repo: "flatrun/templates", Enabled: true, BaseURL: srv.URL}), CacheDir: cache}
	compose := filepath.Join(cache, "wordpress", "docker-compose.yml")

	if _, n, err := s.Sync(context.Background()); err != nil || n != 1 || stub.downloads != 1 {
		t.Fatalf("first sync = %d, %v, downloads %d", n, err, stub.downloads)
	}
	if err := os.WriteFile(compose, []byte("edited locally"), 0o644); err != nil {
		t.Fatal(err)
	}
	if src, n, err := s.Sync(context.Background()); err != nil || src != "github" || n != 0 || stub.downloads != 1 {
		t.Fatalf("unchanged sync = %q, %d, %v, downloads %d", src, n, err, stub.downloads)
	}
	if got, _ := os.ReadFile(compose); string(got) != "edited locally" {
		t.Fatalf("unchanged sync rewrote the cache: %q", got)
	}

	if _, n, err := s.Refresh(context.Background()); err != nil || n != 1 || stub.downloads != 2 {
		t.Fatalf("refresh = %d, %v, downloads %d", n, err, stub.downloads)
	}
	if got, _ := os.ReadFile(compose); string(got) != "services:\n  wordpress:\n" {
		t.Fatalf("refresh left %q", got)
	}

	stub.archive = tarball(t, "templates-main", map[string]string{"wordpress/docker-compose.yml": "services:\n  wordpress:\n    image: wordpress:6\n"})
	stub.etag = `"5d1f0c"`
	if _, n, err := s.Sync(context.Background()); err != nil || n != 1 || stub.downloads != 3 {
		t.Fatalf("changed sync = %d, %v, downloads %d", n, err, stub.downloads)
	}
	if got, _ := os.ReadFile(compose); !strings.Contains(string(got), "wordpress:6") {
		t.Fatalf("changed sync left %q", got)
	}
}

func TestSyncerRetriesAfterAPartialWrite(t *testing.T) {
	stub := &codeloadStub{
		archive: tarball(t, "templates-main", map[string]string{
			"wordpress/docker-compose.yml": "services:\n  wordpress:\n",
			"infra/docker-compose.yml":     "services:\n  nginx:\n",
		}),
		etag: `"a1"`,
	}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	s := &Syncer{Resolver: NewResolver(GitHubSource{Repo: "flatrun/templates", Enabled: true, BaseURL: srv.URL}), CacheDir: t.TempDir()}

	_, _, _ = s.Sync(context.Background())
	_, _, _ = s.Sync(context.Background())
	if stub.downloads != 2 {
		t.Fatalf("downloads = %d: a sync that skipped a template was recorded as complete", stub.downloads)
	}
}

func TestSyncerRestoresAFileRemovedFromTheCache(t *testing.T) {
	stub := &codeloadStub{
		archive: tarball(t, "templates-main", map[string]string{
			"static/docker-compose.yml": "services:\n  web:\n",
			"static/html/index.html":    "<h1>hi</h1>",
		}),
		etag: `"a1"`,
	}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	cache := t.TempDir()
	s := &Syncer{Resolver: NewResolver(GitHubSource{Repo: "flatrun/templates", Enabled: true, BaseURL: srv.URL}), CacheDir: cache}
	page := filepath.Join(cache, "static", "html", "index.html")

	if _, _, err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(page); err != nil {
		t.Fatal(err)
	}
	if _, n, err := s.Sync(context.Background()); err != nil || n != 1 || stub.downloads != 2 {
		t.Fatalf("sync after a removed file = %d, %v, downloads %d", n, err, stub.downloads)
	}
	if got, _ := os.ReadFile(page); string(got) != "<h1>hi</h1>" {
		t.Fatalf("removed file not restored: %q", got)
	}
	if _, _, _ = s.Sync(context.Background()); stub.downloads != 2 {
		t.Fatalf("downloads = %d after the cache was whole again", stub.downloads)
	}
}

func TestSyncIfStaleChecksAtMostOncePerWindow(t *testing.T) {
	stub := &codeloadStub{archive: tarball(t, "templates-main", map[string]string{"static/docker-compose.yml": "services:\n  web:\n"}), etag: `"a1"`}
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		stub.ServeHTTP(w, r)
	}))
	defer srv.Close()
	s := &Syncer{Resolver: NewResolver(GitHubSource{Repo: "flatrun/templates", Enabled: true, BaseURL: srv.URL}), CacheDir: t.TempDir()}

	s.SyncIfStale(context.Background(), time.Minute)
	s.SyncIfStale(context.Background(), time.Minute)
	if requests != 1 {
		t.Fatalf("requests within the window = %d, want 1", requests)
	}
	s.lastCheck = time.Now().Add(-2 * time.Minute)
	s.SyncIfStale(context.Background(), time.Minute)
	if requests != 2 || stub.downloads != 1 {
		t.Fatalf("after the window: requests %d, downloads %d", requests, stub.downloads)
	}

	s.mu.Lock()
	s.lastCheck = time.Time{}
	s.SyncIfStale(context.Background(), time.Minute)
	s.mu.Unlock()
	if requests != 2 {
		t.Fatal("SyncIfStale waited for a sync already in progress")
	}
}
