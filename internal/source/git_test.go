package source

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// makeRepo creates a local git repository with the given files (relative path ->
// content) and returns its path. Tests clone from it over file:// so no network
// is involved.
func makeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	run("init", "-b", "main")
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-m", "init")
	return dir
}

func TestGitProvider_FetchLocalRepo(t *testing.T) {
	repo := makeRepo(t, map[string]string{
		"docker-compose.yml": "services:\n  app:\n    image: nginx\n",
		"README.md":          "hello",
	})

	dest := filepath.Join(t.TempDir(), "src")
	res, err := GitProvider{}.Fetch(context.Background(),
		Descriptor{Type: "git", Ref: "file://" + repo, Params: map[string]string{"branch": "main"}},
		dest, nil)
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(res.Dir, "docker-compose.yml")); err != nil {
		t.Errorf("compose file missing from fetched source: %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "README.md")); err != nil {
		t.Errorf("source file missing from fetched source: %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, ".git")); !os.IsNotExist(err) {
		t.Error(".git should be stripped from the fetched source")
	}
	if res.Revision == "" {
		t.Error("expected a resolved revision")
	}
}

func TestGitProvider_Subpath(t *testing.T) {
	repo := makeRepo(t, map[string]string{
		"deploy/docker-compose.yml": "services:\n  app:\n    image: nginx\n",
		"src/main.go":               "package main",
	})

	dest := filepath.Join(t.TempDir(), "src")
	res, err := GitProvider{}.Fetch(context.Background(),
		Descriptor{Type: "git", Ref: "file://" + repo, Params: map[string]string{"subpath": "deploy"}},
		dest, nil)
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "docker-compose.yml")); err != nil {
		t.Errorf("compose file missing under subpath: %v", err)
	}
}

func TestGitProvider_MissingURL(t *testing.T) {
	_, err := GitProvider{}.Fetch(context.Background(), Descriptor{Type: "git"}, t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected an error for a git source with no URL")
	}
}

func TestAuthenticatedURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		auth *Auth
		want string
	}{
		{"no auth leaves url alone", "https://github.com/o/r.git", nil, "https://github.com/o/r.git"},
		{"empty token leaves url alone", "https://github.com/o/r.git", &Auth{}, "https://github.com/o/r.git"},
		{"token injected as userinfo", "https://github.com/o/r.git", &Auth{Token: "secret"}, "https://oauth2:secret@github.com/o/r.git"},
		{"username honoured", "https://github.com/o/r.git", &Auth{Username: "me", Token: "secret"}, "https://me:secret@github.com/o/r.git"},
		{"ssh untouched", "git@github.com:o/r.git", &Auth{Token: "secret"}, "git@github.com:o/r.git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := authenticatedURL(tt.raw, tt.auth)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("authenticatedURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestResolveSubpath_RejectsEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := resolveSubpath(root, "../../etc"); err == nil {
		t.Fatal("expected an escaping subpath to be rejected")
	}
}

func TestScrubRemovesToken(t *testing.T) {
	got := scrub("cloning https://oauth2:secret@github.com/o/r.git", &Auth{Token: "secret"})
	if got == "cloning https://oauth2:secret@github.com/o/r.git" {
		t.Fatal("token was not scrubbed")
	}
}
