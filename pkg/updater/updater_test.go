package updater

import "testing"

func TestSelectRelease_StableSkipsPrereleases(t *testing.T) {
	releases := []Release{
		{TagName: "v0.3.0-beta.1", Prerelease: true},
		{TagName: "v0.2.0", Prerelease: false},
	}

	got := selectRelease(releases, ChannelStable)
	if got == nil {
		t.Fatal("stable channel found no release")
	}
	if got.TagName != "v0.2.0" {
		t.Errorf("stable channel selected %q, want v0.2.0", got.TagName)
	}
}

func TestSelectRelease_PrereleaseChannelSeesBetas(t *testing.T) {
	releases := []Release{
		{TagName: "v0.3.0-beta.1", Prerelease: true},
		{TagName: "v0.2.0", Prerelease: false},
	}

	got := selectRelease(releases, ChannelPrerelease)
	if got == nil {
		t.Fatal("prerelease channel found no release")
	}
	if got.TagName != "v0.3.0-beta.1" {
		t.Errorf("prerelease channel selected %q, want v0.3.0-beta.1", got.TagName)
	}
}

func TestSelectRelease_PrefersFinalOverItsPrerelease(t *testing.T) {
	releases := []Release{
		{TagName: "v0.3.0-beta.1", Prerelease: true},
		{TagName: "v0.3.0", Prerelease: false},
	}

	got := selectRelease(releases, ChannelPrerelease)
	if got == nil || got.TagName != "v0.3.0" {
		t.Fatalf("selected %v, want the final v0.3.0", got)
	}
}

func TestSelectRelease_OrdersBySemverNotString(t *testing.T) {
	// String comparison ranks "0.2.0" above "0.10.0" because '2' > '1';
	// semver must pick v0.10.0.
	releases := []Release{
		{TagName: "v0.2.0"},
		{TagName: "v0.10.0"},
	}

	got := selectRelease(releases, ChannelStable)
	if got == nil || got.TagName != "v0.10.0" {
		t.Fatalf("selected %v, want v0.10.0", got)
	}
}

func TestSelectRelease_SkipsDraftsAndUnparseable(t *testing.T) {
	releases := []Release{
		{TagName: "v0.9.0", Draft: true},
		{TagName: "nightly"},
		{TagName: "v0.2.0"},
	}

	got := selectRelease(releases, ChannelStable)
	if got == nil || got.TagName != "v0.2.0" {
		t.Fatalf("selected %v, want v0.2.0 (draft and unparseable skipped)", got)
	}
}

func TestSelectRelease_NoneMatchingReturnsNil(t *testing.T) {
	releases := []Release{
		{TagName: "v0.3.0-beta.1", Prerelease: true},
	}

	if got := selectRelease(releases, ChannelStable); got != nil {
		t.Errorf("stable channel selected %q, want nil", got.TagName)
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		name    string
		latest  string
		current string
		want    bool
	}{
		{"higher patch", "0.3.1", "0.3.0", true},
		{"lower is not an update", "0.3.0", "0.4.0", false},
		{"equal is not an update", "0.4.0-beta.1", "0.4.0-beta.1", false},
		{"final beats its prerelease", "0.4.0", "0.4.0-beta.1", true},
		{"prerelease is older than final", "0.4.0-beta.1", "0.4.0", false},
		{"v prefix tolerated", "v0.4.0", "v0.3.0", true},
		{"unparseable current is updatable", "0.4.0", "dev", true},
		{"unparseable latest is not offered", "garbage", "0.4.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNewer(tt.latest, tt.current); got != tt.want {
				t.Errorf("isNewer(%q, %q) = %v, want %v", tt.latest, tt.current, got, tt.want)
			}
		})
	}
}
