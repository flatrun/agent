package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Entry struct {
	Key         string      `json:"key"`
	Type        string      `json:"type"`
	Value       interface{} `json:"value"`
	Default     interface{} `json:"default,omitempty"`
	Description string      `json:"description,omitempty"`
	Sensitive   bool        `json:"sensitive,omitempty"`
}

var hiddenKeys = map[string]bool{
	"auth.jwt_secret": true,
	"auth.api_keys":   true,
}

func Walk(cfg *Config) []Entry {
	defaults := &Config{}
	setDefaults(defaults)

	current := walkValue(reflect.ValueOf(cfg).Elem(), reflect.TypeOf(*cfg), "")
	defaultMap := walkValueMap(reflect.ValueOf(defaults).Elem(), reflect.TypeOf(*defaults), "")

	out := make([]Entry, 0, len(current))
	for _, e := range current {
		if hiddenKeys[e.Key] {
			e.Sensitive = true
			e.Value = nil
		}
		if d, ok := defaultMap[e.Key]; ok {
			e.Default = d
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func Get(cfg *Config, key string) (Entry, error) {
	for _, e := range Walk(cfg) {
		if e.Key == key {
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("unknown config key %q", key)
}

func Set(cfg *Config, key string, raw interface{}) error {
	if hiddenKeys[key] {
		return fmt.Errorf("config key %q is not editable through this API", key)
	}
	field, err := resolveField(reflect.ValueOf(cfg).Elem(), reflect.TypeOf(*cfg), key)
	if err != nil {
		return err
	}
	if !field.CanSet() {
		return fmt.Errorf("config key %q is read-only", key)
	}
	return assignField(field, raw)
}

func walkValue(v reflect.Value, t reflect.Type, prefix string) []Entry {
	if t == reflect.TypeOf(time.Duration(0)) {
		return []Entry{{Key: prefix, Type: "duration", Value: v.Interface().(time.Duration).String()}}
	}
	if t == reflect.TypeOf(time.Time{}) {
		return []Entry{{Key: prefix, Type: "time", Value: v.Interface()}}
	}

	switch t.Kind() {
	case reflect.Struct:
		var out []Entry
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			tag := yamlKey(f)
			if tag == "-" || tag == "" {
				continue
			}
			child := prefix
			if child != "" {
				child += "."
			}
			child += tag
			out = append(out, walkValue(v.Field(i), f.Type, child)...)
		}
		return out
	case reflect.Ptr:
		if v.IsNil() {
			return []Entry{{Key: prefix, Type: t.Elem().Kind().String(), Value: nil}}
		}
		return walkValue(v.Elem(), t.Elem(), prefix)
	case reflect.Slice:
		return []Entry{{Key: prefix, Type: "slice", Value: v.Interface()}}
	case reflect.Map:
		return []Entry{{Key: prefix, Type: "map", Value: v.Interface()}}
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return []Entry{{Key: prefix, Type: t.Kind().String(), Value: v.Interface()}}
	}
	return nil
}

func walkValueMap(v reflect.Value, t reflect.Type, prefix string) map[string]interface{} {
	out := make(map[string]interface{})
	for _, e := range walkValue(v, t, prefix) {
		out[e.Key] = e.Value
	}
	return out
}

func resolveField(v reflect.Value, t reflect.Type, key string) (reflect.Value, error) {
	if key == "" {
		return reflect.Value{}, fmt.Errorf("empty config key")
	}
	parts := strings.SplitN(key, ".", 2)
	head := parts[0]

	if t.Kind() != reflect.Struct {
		return reflect.Value{}, fmt.Errorf("cannot resolve %q at non-struct", key)
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if yamlKey(f) != head {
			continue
		}
		if len(parts) == 1 {
			return v.Field(i), nil
		}
		return resolveField(v.Field(i), f.Type, parts[1])
	}
	return reflect.Value{}, fmt.Errorf("unknown config key %q", key)
}

func assignField(field reflect.Value, raw interface{}) error {
	var data []byte
	if s, ok := raw.(string); ok {
		data = []byte(s)
	} else {
		encoded, err := yaml.Marshal(raw)
		if err != nil {
			return fmt.Errorf("encode value: %w", err)
		}
		data = encoded
	}
	target := reflect.New(field.Type())
	if err := yaml.Unmarshal(data, target.Interface()); err != nil {
		return fmt.Errorf("decode into %s: %w", field.Type(), err)
	}
	field.Set(target.Elem())
	return nil
}

func yamlKey(f reflect.StructField) string {
	tag := f.Tag.Get("yaml")
	if tag == "" {
		return strings.ToLower(f.Name)
	}
	return strings.SplitN(tag, ",", 2)[0]
}
