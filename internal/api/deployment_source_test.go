package api

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/flatrun/agent/internal/source"
)

func gitRepoWithCompose(t *testing.T) string {
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
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"),
		[]byte("services:\n  app:\n    image: nginx\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "init")
	return dir
}

func TestFetchDeploymentSource_UnknownType(t *testing.T) {
	s := &Server{sourceRegistry: source.NewRegistry(source.GitProvider{})}

	_, err := s.fetchDeploymentSource(context.Background(), &deploymentSource{Type: "svn", Ref: "x"})
	if err == nil {
		t.Fatal("expected an error for an unknown source type")
	}
}

func TestFetchDeploymentSource_Git(t *testing.T) {
	repo := gitRepoWithCompose(t)
	s := &Server{sourceRegistry: source.NewRegistry(source.GitProvider{})}

	fetched, err := s.fetchDeploymentSource(context.Background(), &deploymentSource{
		Type:   "git",
		Ref:    "file://" + repo,
		Branch: "main",
	})
	if err != nil {
		t.Fatalf("fetchDeploymentSource failed: %v", err)
	}
	defer fetched.cleanup()

	if fetched.composeName != "docker-compose.yml" {
		t.Errorf("composeName = %q, want docker-compose.yml", fetched.composeName)
	}
	if fetched.composeContent == "" {
		t.Error("expected compose content to be read from the source")
	}
}

func TestFetchDeploymentSource_NoCompose(t *testing.T) {
	// A repo with no compose file must be rejected, not deployed empty.
	dir := t.TempDir()
	run := func(args ...string) {
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
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "init")

	s := &Server{sourceRegistry: source.NewRegistry(source.GitProvider{})}
	_, err := s.fetchDeploymentSource(context.Background(), &deploymentSource{
		Type: "git", Ref: "file://" + dir, Branch: "main",
	})
	if err == nil {
		t.Fatal("expected an error when the source has no compose file")
	}
}
