package templatesource

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"
)

// maxTemplateFileBytes caps a single file read from the tarball. Templates are a
// few KB; the cap bounds a hostile or corrupt archive.
const maxTemplateFileBytes = 2 << 20 // 2 MiB

// GitHubSource delivers the catalog from a GitHub repository (flatrun/templates)
// by downloading its tarball and reading each directory that contains a
// docker-compose.yml as one template. It shells out to nothing: the fetch is a
// single HTTPS request extracted with the standard library, so the fetch
// mechanism is private to this source and swappable without touching callers.
type GitHubSource struct {
	// Repo is "owner/name", e.g. "flatrun/templates".
	Repo string
	// Ref is a branch, tag, or commit; "main" when empty.
	Ref string
	// Enabled gates the source from config.
	Enabled bool
	// BaseURL overrides the codeload host; tests point it at an httptest server.
	BaseURL string
	// Client is the HTTP client; a default with a timeout is used when nil.
	Client *http.Client
}

func (g GitHubSource) Name() string { return "github" }

func (g GitHubSource) Available(ctx context.Context) bool {
	return g.Enabled && g.Repo != ""
}

func (g GitHubSource) ref() string {
	if g.Ref != "" {
		return g.Ref
	}
	return "main"
}

func (g GitHubSource) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (g GitHubSource) tarballURL() string {
	base := g.BaseURL
	if base == "" {
		base = "https://codeload.github.com"
	}
	return fmt.Sprintf("%s/%s/tar.gz/%s", strings.TrimRight(base, "/"), g.Repo, g.ref())
}

func (g GitHubSource) List(ctx context.Context) ([]Template, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.tarballURL(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: status %d", g.Repo, resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	// Read every regular file into a flat map keyed by its path with the
	// tarball's top directory (<name>-<ref>/) stripped.
	files := map[string][]byte{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := stripTopDir(hdr.Name)
		if name == "" || path.Dir(name) == "." {
			// Repo-root files (README, LICENSE, ...) are not template content.
			continue
		}
		if hdr.Size > maxTemplateFileBytes {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxTemplateFileBytes))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		files[name] = data
	}

	return templatesFromFiles(files), nil
}

// stripTopDir removes the leading "<something>/" component that GitHub tarballs
// wrap every entry in.
func stripTopDir(name string) string {
	name = strings.TrimPrefix(name, "./")
	i := strings.IndexByte(name, '/')
	if i < 0 {
		return ""
	}
	return name[i+1:]
}

// templatesFromFiles groups a flat file map into templates. A template is any
// directory holding a docker-compose.yml; every file beneath it (including files
// in subdirectories) is attached with a path relative to that directory. When
// templates nest, a file is assigned to its most specific (longest) root.
func templatesFromFiles(files map[string][]byte) []Template {
	var roots []string
	for name := range files {
		if path.Base(name) == "docker-compose.yml" {
			roots = append(roots, path.Dir(name))
		}
	}
	sort.Slice(roots, func(i, j int) bool { return len(roots[i]) > len(roots[j]) })

	grouped := make(map[string]map[string][]byte, len(roots))
	for _, r := range roots {
		grouped[r] = map[string][]byte{}
	}
	for name, data := range files {
		for _, root := range roots { // longest first
			prefix := root + "/"
			if strings.HasPrefix(name, prefix) {
				grouped[root][name[len(prefix):]] = data
				break
			}
		}
	}

	out := make([]Template, 0, len(roots))
	for _, root := range roots {
		g := grouped[root]
		compose := g["docker-compose.yml"]
		if len(compose) == 0 {
			continue
		}
		t := Template{ID: root, Compose: compose, Metadata: g["metadata.yml"], Files: map[string][]byte{}}
		for name, data := range g {
			if name == "docker-compose.yml" || name == "metadata.yml" {
				continue
			}
			t.Files[name] = data
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
