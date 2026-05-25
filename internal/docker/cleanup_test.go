package docker

import (
	"testing"
)

func TestSplitRepo(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"nginx:1.20", "nginx"},
		{"ghcr.io/acme/api:sha-abc", "ghcr.io/acme/api"},
		{"registry.example.com:5000/app:v1", "registry.example.com:5000/app"},
		{"nginx", "nginx"},
		{"acme/api@sha256:deadbeef", "acme/api"},
	}
	for _, tt := range tests {
		got := splitRepo(tt.input)
		if got != tt.want {
			t.Errorf("splitRepo(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSelectStaleImages(t *testing.T) {
	hostImages := []imageRecord{
		{id: "id-current", repos: []string{"ghcr.io/acme/api"}, fullRefs: []string{"ghcr.io/acme/api:sha-def"}, bytes: 500},
		{id: "id-old", repos: []string{"ghcr.io/acme/api"}, fullRefs: []string{"ghcr.io/acme/api:sha-abc"}, bytes: 480},
		{id: "id-unrelated", repos: []string{"nginx"}, fullRefs: []string{"nginx:1.20"}, bytes: 142},
		{id: "id-other-deployment", repos: []string{"ghcr.io/acme/api"}, fullRefs: []string{"ghcr.io/acme/api:sha-xyz"}, bytes: 490},
		{id: "id-still-running", repos: []string{"ghcr.io/acme/api"}, fullRefs: []string{"ghcr.io/acme/api:sha-old"}, bytes: 470},
		{id: "id-dangling", bytes: 50},
		{id: "id-multitag", repos: []string{"ghcr.io/acme/api", "ghcr.io/acme/api"}, fullRefs: []string{"ghcr.io/acme/api:sha-abc2", "ghcr.io/acme/api:keep"}, bytes: 460},
	}
	currentRefs := map[string]bool{"ghcr.io/acme/api:sha-def": true, "ghcr.io/acme/api:keep": true}
	currentRepos := map[string]bool{"ghcr.io/acme/api": true}
	inUse := map[string]bool{"id-still-running": true}
	otherDeps := map[string]bool{"ghcr.io/acme/api:sha-xyz": true}

	stale, kept := selectStaleImages(hostImages, currentRefs, currentRepos, inUse, otherDeps)
	if len(stale) != 1 {
		t.Fatalf("expected exactly one stale image, got %d (ids: %v)", len(stale), staleIDs(stale))
	}
	if stale[0].id != "id-old" {
		t.Errorf("expected id-old to be the stale one, got %s", stale[0].id)
	}
	if kept != 6 {
		t.Errorf("expected 6 kept, got %d", kept)
	}
}

func TestSelectStaleImagesNothingToRemove(t *testing.T) {
	host := []imageRecord{
		{id: "id-current", repos: []string{"nginx"}, fullRefs: []string{"nginx:1.21"}, bytes: 142},
	}
	stale, kept := selectStaleImages(host, map[string]bool{"nginx:1.21": true}, map[string]bool{"nginx": true}, nil, nil)
	if len(stale) != 0 {
		t.Fatalf("expected no stale images, got %d", len(stale))
	}
	if kept != 1 {
		t.Errorf("expected 1 kept, got %d", kept)
	}
}

func staleIDs(records []imageRecord) []string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, r.id)
	}
	return out
}
