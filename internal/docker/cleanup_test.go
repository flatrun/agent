package docker

import (
	"testing"
)

func TestParseRefNormalizes(t *testing.T) {
	tests := []struct {
		input    string
		wantRepo string
		wantTag  string
	}{
		{"nginx:1.20", "docker.io/library/nginx", "1.20"},
		{"ghcr.io/acme/api:sha-abc1234", "ghcr.io/acme/api", "sha-abc1234"},
		{"registry.example.com:5000/app:v1", "registry.example.com:5000/app", "v1"},
		{"nginx", "docker.io/library/nginx", ""},
		{"acme/api:1.0", "docker.io/acme/api", "1.0"},
	}
	for _, tt := range tests {
		repo, tag := parseRef(tt.input)
		if repo != tt.wantRepo || tag != tt.wantTag {
			t.Errorf("parseRef(%q) = (%q,%q), want (%q,%q)", tt.input, repo, tag, tt.wantRepo, tt.wantTag)
		}
	}
}

func TestLooksLikeContentHash(t *testing.T) {
	hashLike := []string{
		"sha-abc1234123",
		"sha-0123456789abcdef",
		"abc1234",
		"abcdef0123456789abcdef0123456789abcdef01",
		"sha256:deadbeefdeadbeef",
	}
	for _, tag := range hashLike {
		if !looksLikeContentHash(tag) {
			t.Errorf("expected %q to look like content hash", tag)
		}
	}

	floating := []string{
		"latest", "stable", "edge", "main", "master", "dev",
		"v1.2.3", "1.20", "production", "release-2026-05",
		"",
	}
	for _, tag := range floating {
		if looksLikeContentHash(tag) {
			t.Errorf("did NOT expect %q to look like content hash", tag)
		}
	}
}

func TestSelectStaleImagesContentHashGate(t *testing.T) {
	host := []imageRecord{
		{
			id:       "id-old-hash",
			repos:    []string{"ghcr.io/acme/api"},
			fullRefs: []string{"ghcr.io/acme/api:sha-abc1234"},
			tags:     []string{"sha-abc1234"},
			bytes:    480,
		},
		{
			id:       "id-old-latest",
			repos:    []string{"ghcr.io/acme/api"},
			fullRefs: []string{"ghcr.io/acme/api:latest"},
			tags:     []string{"latest"},
			bytes:    490,
		},
		{
			id:       "id-old-semver",
			repos:    []string{"ghcr.io/acme/api"},
			fullRefs: []string{"ghcr.io/acme/api:v1.2.3"},
			tags:     []string{"v1.2.3"},
			bytes:    470,
		},
	}
	currentRefs := map[string]bool{"ghcr.io/acme/api:sha-def5678": true}
	currentRepos := map[string]bool{"ghcr.io/acme/api": true}

	stale, kept := selectStaleImages(host, currentRefs, currentRepos, nil, nil)
	if len(stale) != 1 || stale[0].id != "id-old-hash" {
		t.Fatalf("expected only id-old-hash stale, got %v", staleIDs(stale))
	}
	if kept != 2 {
		t.Errorf("expected 2 kept (latest + semver), got %d", kept)
	}
}

func TestSelectStaleImagesKeepsCurrentInUseAndOtherDeps(t *testing.T) {
	host := []imageRecord{
		{
			id:       "id-current",
			repos:    []string{"ghcr.io/acme/api"},
			fullRefs: []string{"ghcr.io/acme/api:sha-def5678"},
			tags:     []string{"sha-def5678"},
			bytes:    500,
		},
		{
			id:       "id-still-running",
			repos:    []string{"ghcr.io/acme/api"},
			fullRefs: []string{"ghcr.io/acme/api:sha-aaa1111"},
			tags:     []string{"sha-aaa1111"},
			bytes:    470,
		},
		{
			id:       "id-other-deployment",
			repos:    []string{"ghcr.io/acme/api"},
			fullRefs: []string{"ghcr.io/acme/api:sha-fed3210"},
			tags:     []string{"sha-fed3210"},
			bytes:    490,
		},
		{
			id:       "id-unrelated",
			repos:    []string{"nginx"},
			fullRefs: []string{"nginx:1.20"},
			tags:     []string{"1.20"},
			bytes:    142,
		},
		{id: "id-dangling", bytes: 50},
	}
	currentRefs := map[string]bool{"ghcr.io/acme/api:sha-def5678": true}
	currentRepos := map[string]bool{"ghcr.io/acme/api": true}
	inUse := map[string]bool{"id-still-running": true}
	otherDeps := map[string]bool{"ghcr.io/acme/api:sha-fed3210": true}

	stale, _ := selectStaleImages(host, currentRefs, currentRepos, inUse, otherDeps)
	if len(stale) != 0 {
		t.Fatalf("expected no stale images, got %v", staleIDs(stale))
	}
}

func staleIDs(records []imageRecord) []string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, r.id)
	}
	return out
}
