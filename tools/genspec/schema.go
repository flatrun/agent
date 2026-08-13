package main

import (
	"encoding/json"
	"go/types"
	"strings"
)

type openAPI struct {
	OpenAPI    string                           `json:"openapi"`
	Info       info                             `json:"info"`
	Paths      map[string]map[string]*operation `json:"paths"`
	Components components                       `json:"components"`
}

type info struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Version     string `json:"version"`
}

type components struct {
	Schemas map[string]*schema `json:"schemas"`
}

type operation struct {
	OperationID string              `json:"operationId"`
	Tags        []string            `json:"tags,omitempty"`
	Parameters  []parameter         `json:"parameters,omitempty"`
	RequestBody *requestBody        `json:"requestBody,omitempty"`
	Responses   map[string]response `json:"responses"`
	Extensions  map[string]any      `json:"-"`
}

// MarshalJSON writes the extensions inline, which is where OpenAPI expects x- keys.
func (o operation) MarshalJSON() ([]byte, error) {
	type plain operation
	encoded, err := json.Marshal(plain(o))
	if err != nil {
		return nil, err
	}
	if len(o.Extensions) == 0 {
		return encoded, nil
	}
	var merged map[string]any
	if err := json.Unmarshal(encoded, &merged); err != nil {
		return nil, err
	}
	for key, value := range o.Extensions {
		merged[key] = value
	}
	return json.Marshal(merged)
}

type parameter struct {
	Name     string  `json:"name"`
	In       string  `json:"in"`
	Required bool    `json:"required,omitempty"`
	Schema   *schema `json:"schema,omitempty"`
}

type requestBody struct {
	Required bool                 `json:"required,omitempty"`
	Content  map[string]mediaType `json:"content"`
}

type response struct {
	Description string               `json:"description"`
	Content     map[string]mediaType `json:"content,omitempty"`
}

type mediaType struct {
	Schema *schema `json:"schema,omitempty"`
}

type schema struct {
	Ref        string             `json:"$ref,omitempty"`
	Type       string             `json:"type,omitempty"`
	Format     string             `json:"format,omitempty"`
	Items      *schema            `json:"items,omitempty"`
	Properties map[string]*schema `json:"properties,omitempty"`
	// PropertyOrder is the order the fields are declared in, which is the order a caller reads
	// them in and the order the CLI lays out columns. JSON objects have none of their own.
	PropertyOrder []string `json:"x-property-order,omitempty"`
	// Columns are the fields worth showing when a row of this is printed as a table, named on
	// the type so the choice lives with the data rather than in every client.
	Columns              []string `json:"x-columns,omitempty"`
	Required             []string `json:"required,omitempty"`
	Description          string   `json:"description,omitempty"`
	AdditionalProperties *schema  `json:"additionalProperties,omitempty"`
}

// schemaSet collects the named types the spec refers to, so a type used by twenty endpoints is
// described once.
type schemaSet struct {
	byName map[string]*schema
	seen   map[string]bool
}

func (s *schemaSet) add(t types.Type) *schema {
	built := s.build(t, 0)
	if built == nil {
		return nil
	}
	return built
}

func (s *schemaSet) build(t types.Type, depth int) *schema {
	if t == nil || depth > 12 {
		return nil
	}

	switch typed := t.(type) {
	case *types.Pointer:
		return s.build(typed.Elem(), depth)
	case *types.Named:
		return s.named(typed, depth)
	case *types.Alias:
		return s.build(types.Unalias(typed), depth)
	case *types.Basic:
		return basicSchema(typed)
	case *types.Slice:
		if isByteSlice(typed) {
			return &schema{Type: "string", Format: "byte"}
		}
		return &schema{Type: "array", Items: s.build(typed.Elem(), depth+1)}
	case *types.Array:
		return &schema{Type: "array", Items: s.build(typed.Elem(), depth+1)}
	case *types.Map:
		return &schema{Type: "object", AdditionalProperties: s.build(typed.Elem(), depth+1)}
	case *types.Struct:
		return s.structSchema(typed, depth)
	case *types.Interface:
		return &schema{}
	}
	return nil
}

func (s *schemaSet) named(t *types.Named, depth int) *schema {
	obj := t.Obj()
	if obj.Pkg() == nil {
		return s.build(t.Underlying(), depth)
	}

	// time.Time is a struct, but nobody wants its fields.
	if obj.Pkg().Path() == "time" && obj.Name() == "Time" {
		return &schema{Type: "string", Format: "date-time"}
	}
	if obj.Pkg().Path() == "time" && obj.Name() == "Duration" {
		return &schema{Type: "string", Description: "Duration, such as 30s or 1h0m0s"}
	}

	if _, ok := t.Underlying().(*types.Struct); !ok {
		return s.build(t.Underlying(), depth)
	}

	name := schemaName(obj.Pkg().Path(), obj.Name())
	if !s.seen[name] {
		s.seen[name] = true
		// Registered before its fields are walked, so a type holding itself terminates.
		s.byName[name] = &schema{Type: "object"}
		built := s.structSchema(t.Underlying().(*types.Struct), depth+1)
		if built != nil {
			s.byName[name] = built
		}
	}
	return &schema{Ref: "#/components/schemas/" + name}
}

func (s *schemaSet) structSchema(t *types.Struct, depth int) *schema {
	out := &schema{Type: "object", Properties: map[string]*schema{}}
	s.fields(t, depth, out)
	if len(out.Properties) == 0 {
		return &schema{Type: "object"}
	}
	return out
}

func (s *schemaSet) fields(t *types.Struct, depth int, out *schema) {
	for i := 0; i < t.NumFields(); i++ {
		field := t.Field(i)
		tag := parseTag(t.Tag(i))

		if field.Embedded() && tag.name == "" {
			// An embedded struct's fields belong to the outer object.
			if embedded, ok := underlyingStruct(field.Type()); ok {
				s.fields(embedded, depth, out)
				continue
			}
		}
		if !field.Exported() || tag.skip {
			continue
		}

		name := tag.name
		if name == "" {
			name = field.Name()
		}
		built := s.build(field.Type(), depth+1)
		if built == nil {
			continue
		}
		if tag.column {
			out.Columns = append(out.Columns, name)
		}
		out.Properties[name] = built
		out.PropertyOrder = append(out.PropertyOrder, name)
		if tag.required {
			out.Required = append(out.Required, name)
		}
	}
}

func underlyingStruct(t types.Type) (*types.Struct, bool) {
	switch typed := t.(type) {
	case *types.Pointer:
		return underlyingStruct(typed.Elem())
	case *types.Named:
		st, ok := typed.Underlying().(*types.Struct)
		return st, ok
	case *types.Struct:
		return typed, true
	}
	return nil, false
}

type fieldTag struct {
	name     string
	skip     bool
	required bool
	column   bool
}

func parseTag(raw string) fieldTag {
	tag := fieldTag{}
	jsonTag := structTag(raw, "json")
	if jsonTag == "-" {
		tag.skip = true
		return tag
	}
	if jsonTag != "" {
		tag.name = strings.Split(jsonTag, ",")[0]
	}
	if strings.Contains(structTag(raw, "binding"), "required") {
		tag.required = true
	}
	if strings.Contains(structTag(raw, "cli"), "column") {
		tag.column = true
	}
	return tag
}

// structTag reads one key out of a raw struct tag without reflect, which needs a live value.
func structTag(raw, key string) string {
	for raw != "" {
		i := 0
		for i < len(raw) && raw[i] == ' ' {
			i++
		}
		raw = raw[i:]
		if raw == "" {
			break
		}
		i = 0
		for i < len(raw) && raw[i] > ' ' && raw[i] != ':' && raw[i] != '"' {
			i++
		}
		if i+1 >= len(raw) || raw[i] != ':' || raw[i+1] != '"' {
			break
		}
		name := raw[:i]
		raw = raw[i+1:]

		i = 1
		for i < len(raw) && raw[i] != '"' {
			if raw[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(raw) {
			break
		}
		value := raw[1:i]
		raw = raw[i+1:]
		if name == key {
			return value
		}
	}
	return ""
}

func basicSchema(t *types.Basic) *schema {
	switch {
	case t.Info()&types.IsBoolean != 0:
		return &schema{Type: "boolean"}
	case t.Info()&types.IsInteger != 0:
		return &schema{Type: "integer"}
	case t.Info()&types.IsFloat != 0:
		return &schema{Type: "number"}
	case t.Info()&types.IsString != 0:
		return &schema{Type: "string"}
	}
	return &schema{}
}

func isByteSlice(t *types.Slice) bool {
	basic, ok := t.Elem().(*types.Basic)
	return ok && basic.Kind() == types.Byte
}

func schemaName(pkgPath, name string) string {
	parts := strings.Split(pkgPath, "/")
	return parts[len(parts)-1] + "." + name
}
