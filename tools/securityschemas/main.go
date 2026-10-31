package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"reflect"

	"github.com/flatrun/agent/internal/securityreview"
	"github.com/flatrun/agent/pkg/securityprofile"
	"github.com/google/jsonschema-go/jsonschema"
)

func main() {
	destination := "schemas/security/v1"
	if len(os.Args) > 1 {
		destination = os.Args[1]
	}
	if err := os.MkdirAll(destination, 0755); err != nil {
		log.Fatal(err)
	}
	for name, value := range map[string]any{"access-event": securityprofile.AccessEvent{}, "profile": securityprofile.Profile{}, "policy": securityprofile.AccessPolicy{}, "collector-result": securityreview.CollectorResult{}, "evidence-envelope": securityreview.Envelope{}, "proposal": securityreview.Proposal{}, "report": securityreview.Report{}} {
		schema, err := jsonschema.ForType(reflect.TypeOf(value), nil)
		if err != nil {
			log.Fatal(err)
		}
		schema.Schema = "https://json-schema.org/draft/2020-12/schema"
		versions := map[string]string{"profile": securityprofile.APIVersion, "collector-result": "flatrun.collector-result/v1", "evidence-envelope": "flatrun.security-evidence/v1", "proposal": "flatrun.security-proposal/v1", "report": "flatrun.security-report/v1"}
		if version, ok := versions[name]; ok {
			value := any(version)
			schema.Properties["api_version"].Const = &value
		}
		if name == "profile" {
			value := any(securityprofile.BaselineVersion)
			schema.Properties["baseline_version"].Const = &value
		}
		constrainProfiles(schema, map[*jsonschema.Schema]bool{})
		schema.ID = "https://flatrun.dev/schemas/security/v1/" + name + ".json"
		data, err := json.MarshalIndent(schema, "", "  ")
		if err != nil {
			log.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(destination, name+".json"), append(data, '\n'), 0644); err != nil {
			log.Fatal(err)
		}
	}
}

func constrainProfiles(schema *jsonschema.Schema, seen map[*jsonschema.Schema]bool) {
	if schema == nil || seen[schema] {
		return
	}
	seen[schema] = true
	if schema.Properties["baseline_version"] != nil && schema.Properties["targets"] != nil && schema.Properties["reviews"] != nil {
		version := any(securityprofile.APIVersion)
		baseline := any(securityprofile.BaselineVersion)
		schema.Properties["api_version"].Const = &version
		schema.Properties["baseline_version"].Const = &baseline
	}
	for _, child := range schema.Properties {
		constrainProfiles(child, seen)
	}
	for _, child := range schema.Defs {
		constrainProfiles(child, seen)
	}
	constrainProfiles(schema.Items, seen)
	constrainProfiles(schema.AdditionalProperties, seen)
	for _, children := range [][]*jsonschema.Schema{schema.AllOf, schema.AnyOf, schema.OneOf, schema.PrefixItems} {
		for _, child := range children {
			constrainProfiles(child, seen)
		}
	}
}
