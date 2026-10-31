package securityprofile

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestUnsupportedTemplateProfileRemainsVisible(t *testing.T) {
	input := []byte("api_version: flatrun.security-profile/v2\nunknown_policy: preserve-me\nreviews:\n  sample:\n    command: legacy-string-command\n")
	var profile Profile
	if err := yaml.Unmarshal(input, &profile); err != nil {
		t.Fatal(err)
	}
	if profile.Validate() == nil {
		t.Fatal("unsupported profile accepted")
	}
	encoded, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "preserve-me") {
		t.Fatal("unsupported profile discarded")
	}
}

func TestYAMLIntegerSignalsAreValid(t *testing.T) {
	p := Draft("php")
	p.Targets["source"] = Target{Service: "frontend", Roots: []string{"/srv/site"}}
	p.Reviews["files"] = Review{Target: "source", Concern: "integrity", Runs: []string{"full"}, Collect: []Collector{{ID: "files", Image: "", Command: []string{"python3", "/collectors/inventory.py"}, SnapshotPaths: []string{"."}, Signals: []Signal{{ID: "indicators", Kind: "json_count_gt", Pointer: "/items/0/indicator_count", Value: 0}}}}}
	encoded, err := yaml.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Profile
	if err = yaml.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if err = decoded.Validate(); err != nil {
		t.Fatal(err)
	}
}
