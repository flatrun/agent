package pluginapi

import "testing"

func TestResourceAccessRoundTrip(t *testing.T) {
	want := ResourceAccess{Grants: []ResourceGrant{{Resource: "deployment", ID: "shop", Level: "write"}}}
	encoded, err := EncodeResourceAccess(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeResourceAccess(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Allows("deployment", "shop", "read") || !got.Allows("deployment", "shop", "write") {
		t.Fatal("write grant should allow deployment reads and writes")
	}
	if got.Allows("deployment", "other", "read") || got.Allows("deployment", "shop", "admin") {
		t.Fatal("grant must not widen its resource or level")
	}
}
