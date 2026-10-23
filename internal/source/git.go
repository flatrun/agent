package source

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitProvider fetches a source by shallow-cloning a git repository. It shells out
// to the git binary rather than embedding a git implementation, matching how the
// agent already drives docker and nftables.
type GitProvider struct{}

func (GitProvider) Type() string { return "git" }

// Fetch shallow-clones d.Ref into destDir. A branch and a subpath within the
// repository are honoured through d.Params. Private repositories authenticate
// with d.Auth, injected into the clone URL. The .git directory is removed so the
// deployment stays a plain directory of files with no VCS metadata.
func (GitProvider) Fetch(ctx context.Context, d Descriptor, destDir string, log func(string)) (*Result, error) {
	if strings.TrimSpace(d.Ref) == "" {
		return nil, fmt.Errorf("git source requires a repository URL")
	}

	cloneURL, err := authenticatedURL(d.Ref, d.Auth)
	if err != nil {
		return nil, err
	}

	if revision := d.Params["revision"]; revision != "" {
		// clone cannot target a commit, so a pinned revision is fetched alone
		// into an empty repository.
		if err := os.MkdirAll(destDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create clone directory: %w", err)
		}
		steps := [][]string{
			{"-C", destDir, "init", "-q"},
			{"-C", destDir, "fetch", "-q", "--depth", "1", cloneURL, revision},
			{"-C", destDir, "checkout", "-q", "FETCH_HEAD"},
		}
		for _, args := range steps {
			if err := runGit(ctx, args, d.Auth, log); err != nil {
				return nil, err
			}
		}
	} else {
		args := []string{"clone", "--depth", "1"}
		if branch := d.Params["branch"]; branch != "" {
			args = append(args, "--branch", branch)
		}
		args = append(args, cloneURL, destDir)
		if err := runGit(ctx, args, d.Auth, log); err != nil {
			return nil, err
		}
	}

	revision := gitRevision(ctx, destDir)
	message := gitSubject(ctx, destDir)
	_ = os.RemoveAll(filepath.Join(destDir, ".git"))

	contentDir, err := resolveSubpath(destDir, d.Params["subpath"])
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(contentDir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("subpath %q not found in repository", d.Params["subpath"])
	}

	return &Result{Dir: contentDir, Revision: revision, Message: message}, nil
}

// authenticatedURL injects auth into an https clone URL. It leaves ssh and other
// schemes untouched, since those authenticate out of band (an agent key), and it
// leaves the URL alone when no token is supplied.
func authenticatedURL(raw string, auth *Auth) (string, error) {
	if auth == nil || auth.Token == "" {
		return raw, nil
	}
	// Only http(s) URLs carry credentials in the URL. ssh and scp-style remotes
	// (git@host:path) authenticate out of band and do not even parse as URLs.
	if !strings.HasPrefix(raw, "https://") && !strings.HasPrefix(raw, "http://") {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid repository URL: %w", err)
	}
	username := auth.Username
	if username == "" {
		// A bare token authenticates as the user it belongs to; git only needs a
		// non-empty username alongside it, and this value is conventional.
		username = "oauth2"
	}
	u.User = url.UserPassword(username, auth.Token)
	return u.String(), nil
}

// resolveSubpath returns the directory for an optional in-repo subpath, rejecting
// any path that would escape the clone.
func resolveSubpath(root, subpath string) (string, error) {
	if subpath == "" {
		return root, nil
	}
	target := filepath.Join(root, subpath)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("subpath %q escapes the repository", subpath)
	}
	return target, nil
}

func runGit(ctx context.Context, args []string, auth *Auth, log func(string)) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	// Never block on an interactive credential or host-key prompt: a private
	// repo with no or wrong auth must fail fast, not hang the request.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if log != nil {
		if line := strings.TrimSpace(scrub(string(out), auth)); line != "" {
			log(line)
		}
	}
	if err != nil {
		return fmt.Errorf("git %s failed: %s", gitVerb(args), scrub(strings.TrimSpace(string(out)), auth))
	}
	return nil
}

func gitVerb(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-C" {
			i++
			continue
		}
		return args[i]
	}
	return ""
}

func gitRevision(ctx context.Context, dir string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitSubject(ctx context.Context, dir string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "log", "-1", "--format=%s").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// scrub removes the auth token from text before it is logged or returned, so a
// clone URL echoed in git output never carries the secret onward.
func scrub(text string, auth *Auth) string {
	if auth != nil && auth.Token != "" {
		text = strings.ReplaceAll(text, auth.Token, "***")
	}
	return text
}
