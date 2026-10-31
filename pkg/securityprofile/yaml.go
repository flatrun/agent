package securityprofile

import (
	"bytes"
	"encoding/json"

	"gopkg.in/yaml.v3"
)

func (p *Profile) UnmarshalYAML(node *yaml.Node) error {
	type wire Profile
	var raw map[string]any
	if err := node.Decode(&raw); err != nil {
		return err
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var value wire
	if err = decoder.Decode(&value); err != nil {
		*p = Profile{Raw: raw}
		if version, ok := raw["api_version"].(string); ok {
			p.APIVersion = version
		}
		return nil
	}
	*p = Profile(value)
	return nil
}
func (p Profile) MarshalYAML() (any, error) {
	return YAMLValue(p)
}

func YAMLValue(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var node yaml.Node
	if err = yaml.Unmarshal(encoded, &node); err != nil {
		return nil, err
	}
	return node.Content[0], nil
}
func (p Profile) MarshalJSON() ([]byte, error) {
	if p.Raw != nil {
		return json.Marshal(p.Raw)
	}
	type wire Profile
	return json.Marshal(wire(p))
}
