package templatesource

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
