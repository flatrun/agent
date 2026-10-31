package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/flatrun/agent/internal/securityreview"
	"github.com/flatrun/agent/pkg/securityprofile"
	"github.com/google/jsonschema-go/jsonschema"
)

func TestEmbeddedProfileVersionsAreConstrained(t *testing.T) {
	schema, err := jsonschema.ForType(reflect.TypeFor[securityreview.Proposal](), nil)
	if err != nil {
		t.Fatal(err)
	}
	constrainProfiles(schema, map[*jsonschema.Schema]bool{})
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	value := securityreview.Proposal{Profile: securityprofile.Draft("unknown")}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var instance map[string]any
	if err = json.Unmarshal(data, &instance); err != nil {
		t.Fatal(err)
	}
	if err = resolved.Validate(instance); err != nil {
		t.Fatal("valid proposal rejected", err)
	}
	profile := instance["profile"].(map[string]any)
	for key, invalid := range map[string]string{"api_version": "unsupported", "baseline_version": "unsupported"} {
		original := profile[key]
		profile[key] = invalid
		if resolved.Validate(instance) == nil {
			t.Fatalf("invalid embedded %s accepted", key)
		}
		profile[key] = original
	}
}
